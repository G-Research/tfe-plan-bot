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

	LogKeyGitHubSHA = "github_sha"
)

type Base struct {
	githubapp.ClientCreator

	Installations     githubapp.InstallationsService
	ConfigFetcher     *ConfigFetcher
	BaseConfig        *baseapp.HTTPConfig
	TFEClientProvider *plan.ClientProvider
	HTTPClient        *http.Client
	PullOpts          *PullEvaluationOptions

	AppName string
}

type PullEvaluationOptions struct {
	ConfigPath string `yaml:"config_path"`

	// StatusCheckContext will be used to create the status context. It will be used in the following
	// pattern: <StatusCheckContext>/<TFE Organization Name>/<TFE Workspace Name>
	StatusCheckContext string `yaml:"status_check_context"`

	// This field is unused but is left to avoid breaking configuration files:
	// yaml.UnmarshalStrict returns an error for unmapped fields
	//
	// TODO(jgiannuzzi): remove in version 1.0
	Deprecated_AppName string `yaml:"app_name"`
}

func (p *PullEvaluationOptions) FillDefaults() {
	if p.ConfigPath == "" {
		p.ConfigPath = DefaultPolicyPath
	}

	if p.StatusCheckContext == "" {
		p.StatusCheckContext = DefaultStatusCheckContext
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

func (b *Base) Evaluate(ctx context.Context, installationID int64, trigger common.Trigger, loc pull.Locator) error {
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

	return b.EvaluateFetchedConfig(ctx, prctx, client, fetchedConfig, trigger)
}

func (b *Base) EvaluateFetchedConfig(ctx context.Context, prctx pull.Context, client *github.Client, fetchedConfig FetchedConfig, trigger common.Trigger) error {
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

			if err := b.EvaluateWorkspace(ctx, prctx, client, fetchedConfig, trigger, wkcfg); err != nil {
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

func (b *Base) EvaluateWorkspace(ctx context.Context, prctx pull.Context, client *github.Client, fetchedConfig FetchedConfig, trigger common.Trigger, wkcfg plan.WorkspaceConfig) error {
	logger := zerolog.Ctx(ctx)

	pc, err := plan.NewContext(ctx, wkcfg, prctx, fetchedConfig.Config, b.statusCheckContext(wkcfg), client, b.TFEClientProvider)
	if err != nil {
		statusMessage := fmt.Sprintf("Invalid workspace %s defined by %s", wkcfg, fetchedConfig)
		logger.Warn().Err(err).Msg(statusMessage)
		err := b.PostStatus(ctx, prctx, wkcfg, "", client, "error", statusMessage)
		return err
	}

	policyTrigger := pc.Trigger()
	if !trigger.Matches(policyTrigger) {
		logger.Debug().
			Str("event_trigger", trigger.String()).
			Str("policy_trigger", policyTrigger.String()).
			Msg("No evaluation necessary for this trigger, skipping")
		return nil
	}

	result := pc.Evaluate()
	if result.Error != nil {
		statusMessage := fmt.Sprintf("Error evaluating workspace %s defined by %s", wkcfg, fetchedConfig)
		logger.Warn().Err(result.Error).Msg(statusMessage)
		err := b.PostStatus(ctx, prctx, wkcfg, "", client, "error", statusMessage)
		return err
	}

	statusDescription := result.StatusDescription
	var statusState string
	var runID string
	switch result.Status {
	case plan.StatusSkipped:
		logger.Debug().Msgf("Workspace %s skipped", wkcfg)
		if !wkcfg.ShowSkipped {
			return nil
		}
		statusState = "success"
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

	if err := b.PostStatus(ctx, prctx, wkcfg, runID, client, statusState, statusDescription); err != nil {
		return err
	}

	if result.Status == plan.StatusPolicyPending && !prctx.IsDraft() {
		if reqs := reviewer.FindRequests(&result.PolicyResult); len(reqs) > 0 {
			logger.Debug().Msgf("Found %d pending rules with review requests enabled", len(reqs))
			return b.requestReviews(ctx, prctx, client, reqs)
		}
		logger.Debug().Msgf("No pending rules have review requests enabled, skipping reviewer assignment")
	}

	return nil
}

func (b *Base) requestReviews(ctx context.Context, prctx pull.Context, client *github.Client, reqs []*common.Result) error {
	const maxDelayMillis = 2500

	logger := zerolog.Ctx(ctx)

	// Seed the random source with the PR creation time so that repeated
	// evaluations produce the same set of reviewers. This is required to avoid
	// duplicate requests on later evaluations.
	r := rand.New(rand.NewSource(prctx.CreatedAt().UnixNano()))
	selection, err := reviewer.SelectReviewers(ctx, prctx, reqs, r)
	if err != nil {
		return errors.Wrap(err, "failed to select reviewers")
	}

	if selection.IsEmpty() {
		logger.Debug().Msg("No eligible users or teams found for review")
		return nil
	}

	// This is a terrible strategy to avoid conflicts between closely spaced
	// events assigning the same reviewers, but I expect it to work alright in
	// practice and it avoids any kind of coordination backend:
	//
	// Wait a random amount of time to space out events then check for existing
	// reviewers and apply the difference. The idea is to order two competing
	// events such that one observes the applied reviewers of the other.
	//
	// Use the global random source instead of the per-PR source so that two
	// events for the same PR don't wait for the same amount of time.
	delay := time.Duration(rand.Intn(maxDelayMillis)) * time.Millisecond
	logger.Debug().Msgf("Waiting for %s to spread out reviewer processing", delay)
	time.Sleep(delay)

	// check again if someone assigned a reviewer while we were calculating users to request
	reviewers, err := prctx.RequestedReviewers()
	if err != nil {
		return err
	}

	if diff := selection.Difference(reviewers); !diff.IsEmpty() {
		req := selectionToReviewersRequest(diff)
		logger.Debug().
			Strs("users", req.Reviewers).
			Strs("teams", req.TeamReviewers).
			Msgf("Requesting reviews from %d users and %d teams", len(req.Reviewers), len(req.TeamReviewers))

		_, _, err = client.PullRequests.RequestReviewers(ctx, prctx.RepositoryOwner(), prctx.RepositoryName(), prctx.Number(), req)
		return errors.Wrap(err, "failed to request reviewers")
	}

	logger.Debug().Msg("All selected reviewers are already assigned or were explicitly removed")
	return nil
}

func selectionToReviewersRequest(s reviewer.Selection) github.ReviewersRequest {
	req := github.ReviewersRequest{}

	if len(s.Users) > 0 {
		req.Reviewers = s.Users
	} else {
		req.Reviewers = []string{}
	}

	if len(s.Teams) > 0 {
		req.TeamReviewers = s.Teams
	} else {
		req.TeamReviewers = []string{}
	}

	return req
}
