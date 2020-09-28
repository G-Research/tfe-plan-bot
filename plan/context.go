// Copyright 2020 G-Research Limited
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plan

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/go-github/v32/github"
	"github.com/hashicorp/go-tfe"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/palantir/policy-bot/policy"
	"github.com/palantir/policy-bot/policy/common"

	"github.com/G-Research/tfe-plan-bot/pull"
)

const LogKeyTFEWorkspace = "tfe_workspace"

type Context struct {
	ctx       context.Context
	wkcfg     WorkspaceConfig
	prctx     pull.Context
	evaluator common.Evaluator

	ghClient *github.Client

	tfeClient  *tfe.Client
	tfeAddress string

	statusCtx string
}

func NewContext(ctx context.Context, wkcfg WorkspaceConfig, prctx pull.Context, cfg *Config, statusCtx string, v3client *github.Client, tp *ClientProvider) (*Context, error) {
	evaluator, err := policy.ParsePolicy(&policy.Config{
		Policy:        wkcfg.Policy,
		ApprovalRules: cfg.ApprovalRules,
	})
	if err != nil {
		return nil, errors.Wrap(err, "invalid policy")
	}

	tfeclient, err := tp.Client(wkcfg.Organization)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get TFE client")
	}

	logger := zerolog.Ctx(ctx).With().Str(LogKeyTFEWorkspace, wkcfg.String()).Logger()

	return &Context{
		ctx:       logger.WithContext(ctx),
		wkcfg:     wkcfg,
		prctx:     prctx,
		evaluator: evaluator,

		ghClient: v3client,

		tfeClient:  tfeclient,
		tfeAddress: tp.Address(),

		statusCtx: statusCtx,
	}, nil
}

func (pc *Context) Evaluate() Result {
	logger := zerolog.Ctx(pc.ctx)

	ok, err := pc.matchPR()
	if err != nil {
		return Result{Error: err}
	}
	if !ok {
		return Result{Status: StatusSkipped}
	}

	polRes := pc.evaluator.Evaluate(pc.ctx, pc.prctx)
	if polRes.Error != nil {
		return Result{Error: errors.Wrap(polRes.Error, "failed to evaluate policy")}
	}
	switch polRes.Status {
	case common.StatusApproved:
		logger.Debug().Msg("policy approved")
	case common.StatusSkipped:
		return Result{Error: errors.New("all policy rules were skipped")}
	case common.StatusPending:
		return Result{Status: StatusPolicyPending, Description: polRes.Description, PolicyResult: polRes}
	case common.StatusDisapproved:
		return Result{Status: StatusPolicyDisapproved, Description: polRes.Description, PolicyResult: polRes}
	default:
		return Result{Error: errors.Errorf("policy evaluation resulted in unexpected state: %s", polRes.Status)}
	}

	wk, err := pc.tfeClient.Workspaces.Read(pc.ctx, pc.wkcfg.Organization, pc.wkcfg.Name)
	if err != nil {
		return Result{Error: errors.Wrap(err, "failed to read workspace")}
	}

	if err := pc.validate(wk); err != nil {
		return Result{Error: err}
	}

	statuses, err := pc.prctx.LatestDetailedStatuses()
	if err != nil {
		return Result{Error: errors.Wrap(err, "failed to get latest statuses")}
	}

	status := statuses[pc.statusCtx]
	if status != nil && status.TargetURL != nil && strings.HasPrefix(*status.TargetURL, pc.tfeAddress) {
		url := *status.TargetURL
		runID := url[strings.LastIndex(url, "/")+1:]
		result := Result{RunID: runID}
		switch *status.State {
		case "pending":
			result.Status = StatusPlanPending
		default:
			result.Status = StatusPlanDone
		}
		return result
	}

	codePath, cleanup, err := pc.prctx.DownloadCode()
	if err != nil {
		return Result{Error: errors.Wrap(err, "failed to download code")}
	}
	defer cleanup()

	cv, err := pc.tfeClient.ConfigurationVersions.Create(pc.ctx, wk.ID, tfe.ConfigurationVersionCreateOptions{
		AutoQueueRuns: tfe.Bool(false),
		Speculative:   tfe.Bool(true),
	})
	if err != nil {
		return Result{Error: errors.Wrap(err, "failed to create configuration version")}
	}

	if err := pc.tfeClient.ConfigurationVersions.Upload(pc.ctx, cv.UploadURL, codePath); err != nil {
		return Result{Error: errors.Wrap(err, "failed to upload configuration version")}
	}

	if err := pc.waitForConfigurationVersionUpload(cv.ID, 30*time.Second); err != nil {
		return Result{Error: errors.Wrap(err, "failed to upload configuration version")}
	}

	run, err := pc.tfeClient.Runs.Create(pc.ctx, tfe.RunCreateOptions{
		Message:              tfe.String(pc.runMessage()),
		ConfigurationVersion: cv,
		Workspace:            wk,
	})
	if err != nil {
		return Result{Error: errors.Wrap(err, "failed to create speculative plan")}
	}

	logger.Debug().Msgf("speculative plan created with ID %s", run.ID)
	return Result{
		Status:       StatusPlanCreated,
		Description:  "Terraform plan: pending",
		PolicyResult: polRes,
		RunID:        run.ID,
	}
}

func (pc *Context) MonitorRun(ctx context.Context, poster StatusPoster, runID string) {
	go func() {
		logger := zerolog.Ctx(pc.ctx)

		for {
			select {
			case <-ctx.Done():
				logger.Debug().Msgf("Monitoring stopped for run with ID %s", runID)
				return
			case <-time.After(500 * time.Millisecond):
				r, err := pc.tfeClient.Runs.Read(ctx, runID)
				if err != nil {
					logger.Warn().Err(err).Msgf("Error reading run with ID %s", runID)
				} else {
					var state string
					var message string

					switch r.Status {
					case tfe.RunCanceled:
						state = "error"
						message = "Terraform plan: canceled."
					case tfe.RunErrored:
						state = "failure"
						message = "Terraform plan: errored."
					case tfe.RunPolicySoftFailed:
						state = "failure"
						message = "Terraform plan: policy check failed."
					case tfe.RunPlannedAndFinished:
						state = "success"
						p, err := pc.tfeClient.Plans.Read(ctx, r.Plan.ID)
						if err != nil {
							logger.Warn().Err(err).Msgf("Error reading plan with ID %s", r.Plan.ID)
							message = "Terraform plan: successful."
						} else {
							message = fmt.Sprintf("Terraform plan: %d to add, %d to change, %d to destroy.",
								p.ResourceAdditions,
								p.ResourceChanges,
								p.ResourceDestructions,
							)
						}
					default:
						continue
					}

					logger.Debug().Msgf("%s: %s", state, message)
					poster.PostStatus(ctx, pc.prctx, pc.wkcfg, runID, pc.ghClient, state, message)
					return
				}
			}
		}
	}()
}

func (pc *Context) branch() string {
	if pc.wkcfg.Branch == "" {
		return pc.prctx.DefaultBranch()
	}
	return pc.wkcfg.Branch
}

func (pc *Context) workingDirectory() string {
	return path.Clean(pc.wkcfg.WorkingDirectory)
}

func (pc *Context) runMessage() string {
	_, head := pc.prctx.Branches()
	return fmt.Sprintf("PR #%d: %q (%s@%s)",
		pc.prctx.Number(),
		pc.prctx.Title(),
		head,
		pc.prctx.HeadSHA()[:7],
	)
}

func (pc *Context) matchPR() (bool, error) {
	baseBranch, _ := pc.prctx.Branches()

	if baseBranch != pc.branch() {
		return false, nil
	}

	if pc.workingDirectory() != "." {
		changedFiles, err := pc.prctx.ChangedFiles()
		if err != nil {
			return false, errors.Wrap(err, "failed to get changed files")
		}

		for _, file := range changedFiles {
			if strings.HasPrefix(file.Filename, pc.workingDirectory()+"/") {
				return true, nil
			}
		}

		return false, nil
	}

	return true, nil
}

func (pc *Context) validate(wk *tfe.Workspace) error {
	wkBranch := wk.VCSRepo.Branch
	if wkBranch == "" {
		wkBranch = pc.prctx.DefaultBranch()
	}

	if pc.branch() != wkBranch {
		return errors.Errorf("workspace branch mismatch: config=%q tfe=%q",
			pc.wkcfg.Branch, wk.VCSRepo.Branch)
	}

	if pc.workingDirectory() != path.Clean(wk.WorkingDirectory) {
		return errors.Errorf("workspace working directory mismatch: config=%q tfe=%q",
			pc.wkcfg.WorkingDirectory, wk.WorkingDirectory)
	}

	return nil
}

func (pc *Context) waitForConfigurationVersionUpload(cvID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(pc.ctx, timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-time.After(500 * time.Millisecond):
			cv, err := pc.tfeClient.ConfigurationVersions.Read(ctx, cvID)
			if err != nil {
				return err
			}

			if cv.Status == tfe.ConfigurationUploaded {
				return nil
			}
		}
	}
}
