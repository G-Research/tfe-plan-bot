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

package pull

import (
	"context"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/codeclysm/extract"
	"github.com/google/go-github/v53/github"
	"github.com/pkg/errors"
	"github.com/shurcooL/githubv4"

	"github.com/palantir/policy-bot/pull"
)

// Locator identifies a pull request and optionally contains a full or partial
// pull request object.
type Locator pull.Locator

// IsComplete returns true if the locator contains a pull request object with
// all required fields.
func (loc Locator) IsComplete() bool {
	switch {
	case loc.Value == nil:
	case loc.Value.GetBase().GetRepo().GetDefaultBranch() == "":
	default:
		return true
	}
	return false
}

func (loc Locator) toV4(ctx context.Context, client *githubv4.Client) (*v4PullRequest, error) {
	if !loc.IsComplete() {
		var q struct {
			Repository struct {
				PullRequest v4PullRequest `graphql:"pullRequest(number: $number)"`
			} `graphql:"repository(owner: $owner, name: $name)"`
		}
		qvars := map[string]interface{}{
			"owner":  githubv4.String(loc.Owner),
			"name":   githubv4.String(loc.Repo),
			"number": githubv4.Int(loc.Number),
		}
		if err := client.Query(ctx, &q, qvars); err != nil {
			return nil, errors.Wrap(err, "failed to load pull request details")
		}
		return &q.Repository.PullRequest, nil
	}

	var v4 v4PullRequest
	v4.BaseRepository.DefaultBranchRef.Name = loc.Value.GetBase().GetRepo().GetDefaultBranch()
	return &v4, nil
}

type GitHubContext struct {
	pull.Context

	ctx        context.Context
	client     *github.Client
	pr         *v4PullRequest
	httpClient *http.Client
}

func NewGitHubContext(ctx context.Context, mbrCtx pull.MembershipContext, globalCache pull.GlobalCache, client *github.Client, v4client *githubv4.Client, httpClient *http.Client, loc Locator) (Context, error) {
	ghc, err := pull.NewGitHubContext(ctx, mbrCtx, globalCache, client, v4client, pull.Locator(loc))
	if err != nil {
		return nil, err
	}

	pr, err := loc.toV4(ctx, v4client)
	if err != nil {
		return nil, err
	}

	return &GitHubContext{
		Context:    ghc,
		ctx:        ctx,
		client:     client,
		pr:         pr,
		httpClient: httpClient,
	}, nil
}

func (ghc *GitHubContext) DefaultBranch() string {
	return ghc.pr.BaseRepository.DefaultBranchRef.Name
}

func (ghc *GitHubContext) DownloadCode() (string, func(), error) {
	url, _, err := ghc.client.Repositories.GetArchiveLink(
		ghc.ctx,
		ghc.Context.RepositoryOwner(),
		ghc.Context.RepositoryName(),
		github.Tarball,
		&github.RepositoryContentGetOptions{
			Ref: ghc.Context.HeadSHA(),
		},
		true,
	)
	if err != nil {
		return "", func() {}, errors.Wrap(err, "failed to get code archive link")
	}

	req, err := http.NewRequestWithContext(ghc.ctx, http.MethodGet, url.String(), nil)
	if err != nil {
		return "", func() {}, err
	}

	resp, err := ghc.httpClient.Do(req)
	if err != nil {
		return "", func() {}, errors.Wrap(err, "failed to download code archive")
	}
	defer resp.Body.Close()

	dir, err := ioutil.TempDir("", "tfe-plan-bot")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() {
		os.RemoveAll(dir)
	}

	if err := extract.Gz(ghc.ctx, resp.Body, dir, shiftPath); err != nil {
		return "", cleanup, errors.Wrap(err, "failed to extract code archive")
	}

	return dir, cleanup, nil
}

func (ghc *GitHubContext) LatestDetailedStatuses() (map[string]*github.RepoStatus, error) {
	opt := &github.ListOptions{
		PerPage: 100,
	}
	// get all pages of results
	statuses := make(map[string]*github.RepoStatus)
	for {
		combinedStatus, resp, err := ghc.client.Repositories.GetCombinedStatus(ghc.ctx, ghc.RepositoryOwner(), ghc.RepositoryName(), ghc.HeadSHA(), opt)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get statuses for page %d", opt.Page)
		}
		for _, s := range combinedStatus.Statuses {
			statuses[s.GetContext()] = s
		}
		if resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}
	return statuses, nil
}

func shiftPath(path string) string {
	components := strings.Split(path, "/")
	if len(components) > 1 {
		return filepath.Join(components[1:]...)
	}
	return ""
}

type v4PullRequest struct {
	BaseRepository struct {
		DefaultBranchRef struct {
			Name string
		}
	}
}
