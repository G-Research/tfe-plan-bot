// Copyright 2018 Palantir Technologies, Inc.
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

package handler

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v32/github"
	"github.com/palantir/go-baseapp/baseapp"
	"github.com/palantir/go-githubapp/githubapp"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"

	"github.com/palantir/policy-bot/policy/common"
	"github.com/palantir/policy-bot/policy/reviewer"

	"github.com/G-Research/tfe-plan-bot/plan"
	"github.com/G-Research/tfe-plan-bot/pull"
)

const (
	DefaultPolicyPath         = ".tfe-plan.yml"
	DefaultStatusCheckContext = "TFE"
	DefaultAppName            = "tfe-plan-bot"

	LogKeyGitHubSHA = "github_sha"
)

type Base struct {
	githubapp.ClientCreator

	Installations     githubapp.InstallationsService
	PullOpts          *PullEvaluationOptions
	ConfigFetcher     *ConfigFetcher
	BaseConfig        *baseapp.HTTPConfig
	TFEClientProvider *plan.ClientProvider
	HTTPClient        *http.Client
}

type PullEvaluationOptions struct {
	AppName    string `yaml:"app_name"`
	ConfigPath string `yaml:"config_path"`

	// StatusCheckContext will be used to create the status context. It will be used in the following
	// pattern: <StatusCheckContext>/<TFE Organization Name>/<TFE Workspace Name>
	StatusCheckContext string `yaml:"status_check_context"`
}

func (p *PullEvaluationOptions) FillDefaults() {
	if p.ConfigPath == "" {
		p.ConfigPath = DefaultPolicyPath
	}

	if p.StatusCheckContext == "" {
		p.StatusCheckContext = DefaultStatusCheckContext
	}

	if p.AppName == "" {
		p.AppName = DefaultAppName
	}
}

func (b *Base) PostStatus(ctx context.Context, prctx pull.Context, wkcfg plan.WorkspaceConfig, runID string, client *github.Client, state, message string) error {
	owner := prctx.RepositoryOwner()
	repo := prctx.RepositoryName()
	sha := prctx.HeadSHA()

	status := &github.RepoStatus{
		Context:     github.String(b.statusCheckContext(wkcfg)),
		State:       &state,
		Description: &message,
	}

	if runID != "" {
		status.TargetURL = github.String(b.targetURL(wkcfg, runID))
	}

	if err := b.postGitHubRepoStatus(ctx, client, owner, repo, sha, status); err != nil {
		return err
	}

	return nil
}

func (b *Base) postGitHubRepoStatus(ctx context.Context, client *github.Client, owner, repo, ref string, status *github.RepoStatus) error {
	logger := zerolog.Ctx(ctx)
	logger.Info().Msgf("Setting %q status on %s to %s: %s", status.GetContext(), ref, status.GetState(), status.GetDescription())
	_, _, err := client.Repositories.CreateStatus(ctx, owner, repo, ref, status)
	return err
}

func (b *Base) statusCheckContext(wkcfg plan.WorkspaceConfig) string {
	return fmt.Sprintf("%s/%s", b.PullOpts.StatusCheckContext, wkcfg)
}

func (b *Base) targetURL(wkcfg plan.WorkspaceConfig, runID string) string {
	return fmt.Sprintf("%s/app/%s/workspaces/%s/runs/%s", b.TFEClientProvider.Address(), wkcfg.Organization, wkcfg.Name, runID)
}

func (b *Base) PreparePRContext(ctx context.Context, installationID int64, pr *github.PullRequest) (context.Context, zerolog.Logger) {
	ctx, logger := githubapp.PreparePRContext(ctx, installationID, pr.GetBase().GetRepo(), pr.GetNumber())

	logger = logger.With().Str(LogKeyGitHubSHA, pr.GetHead().GetSHA()).Logger()
	ctx = logger.WithContext(ctx)

	return ctx, logger
}

func (b *Base) Evaluate(ctx context.Context, installationID int64, requestReviews bool, loc pull.Locator) error {
	client, err := b.NewInstallationClient(installationID)
	if err != nil {
		return err
	}

	v4client, err := b.NewInstallationV4Client(installationID)
	if err != nil {
		return err
	}

	mbrCtx := NewCrossOrgMembershipContext(ctx, client, loc.Owner, b.Installations, b.ClientCreator)
	prctx, err := pull.NewGitHubContext(ctx, mbrCtx, client, v4client, b.HTTPClient, loc)
	if err != nil {
		return err
	}

	fetchedConfig, err := b.ConfigFetcher.ConfigForPR(ctx, prctx, client)
	if err != nil {
		return errors.WithMessage(err, fmt.Sprintf("failed to fetch policy: %s", fetchedConfig))
	}

	return b.EvaluateFetchedConfig(ctx, prctx, requestReviews, client, fetchedConfig)
}

func (b *Base) EvaluateFetchedConfig(ctx context.Context, prctx pull.Context, requestReviews bool, client *github.Client, fetchedConfig FetchedConfig) error {
	logger := zerolog.Ctx(ctx)

	if fetchedConfig.Missing() {
		logger.Debug().Msgf("Policy does not exist: %s", fetchedConfig)
		return nil
	}

	if fetchedConfig.Invalid() {
		logger.Warn().Err(fetchedConfig.Error).Msgf("Invalid policy: %s", fetchedConfig)
		return nil
	}

	var wg sync.WaitGroup
	var evaluationFailures uint32
	for i := range fetchedConfig.Config.Workspaces {
		wkcfg := fetchedConfig.Config.Workspaces[i]

		wg.Add(1)

		go func() {
			defer wg.Done()

			if err := b.EvaluateWorkspace(ctx, prctx, requestReviews, client, fetchedConfig, wkcfg); err != nil {
				atomic.AddUint32(&evaluationFailures, 1)
				logger.Error().Err(err).Msgf("Failed to evaluate workspace %s", wkcfg)
			}
		}()
	}

	wg.Wait()

	if evaluationFailures == 0 {
		return nil
	}

	return errors.Errorf("failed to evaluate %d workspaces", evaluationFailures)
}

func (b *Base) EvaluateWorkspace(ctx context.Context, prctx pull.Context, requestReviews bool, client *github.Client, fetchedConfig FetchedConfig, wkcfg plan.WorkspaceConfig) error {
	logger := zerolog.Ctx(ctx)

	pc, err := plan.NewContext(ctx, wkcfg, prctx, fetchedConfig.Config, b.statusCheckContext(wkcfg), client, b.TFEClientProvider)
	if err != nil {
		statusMessage := fmt.Sprintf("Invalid workspace %s defined by %s", wkcfg, fetchedConfig)
		logger.Warn().Err(err).Msg(statusMessage)
		err := b.PostStatus(ctx, prctx, wkcfg, "", client, "error", statusMessage)
		return err
	}

	result := pc.Evaluate()
	if result.Error != nil {
		statusMessage := fmt.Sprintf("Error evaluating workspace %s defined by %s", wkcfg, fetchedConfig)
		logger.Warn().Err(result.Error).Msg(statusMessage)
		err := b.PostStatus(ctx, prctx, wkcfg, "", client, "error", statusMessage)
		return err
	}

	statusDescription := result.Description
	var statusState string
	var runID string
	switch result.Status {
	case plan.StatusSkipped:
		logger.Debug().Msgf("Workspace %s skipped", wkcfg)
		return nil
	case plan.StatusPolicyPending:
		statusState = "pending"
	case plan.StatusPolicyDisapproved:
		statusState = "failure"
	case plan.StatusPlanCreated:
		statusState = "pending"
		runID = result.RunID
		logger.Info().Msgf("Plan created for workspace %s", wkcfg)
		pc.MonitorRun(context.Background(), b, runID)
	case plan.StatusPlanPending:
		logger.Debug().Msgf("Workspace %s already has a plan: %s", wkcfg, result.RunID)
		pc.MonitorRun(context.Background(), b, result.RunID)
		return nil
	case plan.StatusPlanDone:
		logger.Debug().Msgf("Workspace %s already has a completed plan: %s", wkcfg, result.RunID)
		return nil
	default:
		return errors.Errorf("evaluation resulted in unexpected state: %s", result.Status)
	}

	err = b.PostStatus(ctx, prctx, wkcfg, runID, client, statusState, statusDescription)
	if err != nil {
		return err
	}

	if requestReviews && result.Status == plan.StatusPolicyPending && !prctx.IsDraft() {
		if reqs := reviewer.FindRequests(&result.PolicyResult); len(reqs) > 0 {
			logger.Debug().Msgf("Found %d pending rules with review requests enabled", len(reqs))
			return b.requestReviews(ctx, prctx, client, reqs)
		}
		logger.Debug().Msgf("No pending rules have review requests enabled, skipping reviewer assignment")
	}

	return nil
}

func (b *Base) requestReviews(ctx context.Context, prctx pull.Context, client *github.Client, reqs []*common.Result) error {
	logger := zerolog.Ctx(ctx)

	hasReviewers, err := prctx.HasReviewers()
	if err != nil {
		return errors.Wrap(err, "failed to check existing reviewers")
	}
	if hasReviewers {
		logger.Debug().Msg("PR has existing reviewers, skipping reviewer assignment")
		return nil
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	requestedUsers, requestedTeams, err := reviewer.SelectReviewers(ctx, prctx, reqs, r)
	if err != nil {
		return errors.Wrap(err, "failed to select reviewers")
	}

	// check again if someone assigned a reviewer while we were calculating users to request
	hasReviewersAfter, err := prctx.HasReviewers()
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to double-check existing reviewers, assuming there are still no reviewers")
		hasReviewersAfter = false
	}

	if (len(requestedUsers) > 0 || len(requestedTeams) > 0) && !hasReviewersAfter {
		reviewers := github.ReviewersRequest{
			Reviewers:     requestedUsers,
			TeamReviewers: requestedTeams,
		}

		logger.Debug().
			Strs("users", requestedUsers).
			Strs("teams", requestedTeams).
			Msgf("Requesting reviews from %d users and %d teams", len(requestedUsers), len(requestedTeams))

		_, _, err = client.PullRequests.RequestReviewers(ctx, prctx.RepositoryOwner(), prctx.RepositoryName(), prctx.Number(), reviewers)
		if err != nil {
			return errors.Wrap(err, "failed to request reviewers")
		}
	} else {
		logger.Debug().Msg("No eligible users or teams found for review, or reviewers were assigned during processing")
	}
	return nil
}
