// Copyright 2026 G-Research Limited
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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v53/github"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/G-Research/tfe-plan-bot/plan"
	"github.com/G-Research/tfe-plan-bot/pull"
)

// fakePullContext implements pull.Context by embedding the interface itself
// and overriding only the methods PostStatus actually calls. Any other
// method being invoked would panic on the nil embedded interface, which is
// intentional: it would mean PostStatus started depending on something this
// test doesn't know to expect.
type fakePullContext struct {
	pull.Context

	owner string
	repo  string
	sha   string

	statuses    map[string]*github.RepoStatus
	statusesErr error
}

func (f *fakePullContext) RepositoryOwner() string { return f.owner }
func (f *fakePullContext) RepositoryName() string  { return f.repo }
func (f *fakePullContext) HeadSHA() string         { return f.sha }

func (f *fakePullContext) LatestDetailedStatuses() (map[string]*github.RepoStatus, error) {
	return f.statuses, f.statusesErr
}

// newTestGithubClient returns a github.Client pointed at a local test
// server, and a *bool that flips to true if that server ever receives a
// request. This lets tests assert "was CreateStatus called at all" without
// caring about the exact request shape.
func newTestGithubClient(t *testing.T) (client *github.Client, called *bool, closeFn func()) {
	t.Helper()

	wasCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		wasCalled = true
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(&github.RepoStatus{})
	})
	server := httptest.NewServer(mux)

	c := github.NewClient(nil)
	baseURL, err := url.Parse(server.URL + "/")
	require.NoError(t, err)
	c.BaseURL = baseURL

	return c, &wasCalled, server.Close
}

func TestPostStatus(t *testing.T) {
	wkcfg := plan.WorkspaceConfig{Organization: "acme", Name: "prod"}
	// b.statusCheckContext(wkcfg) => "TFE/acme/prod"

	tp, err := plan.NewClientProvider(plan.ClientProviderConfig{Address: "https://tfe.example.com"})
	require.NoError(t, err)

	testCases := []struct {
		name        string
		existing    map[string]*github.RepoStatus
		existingErr error
		runID       string
		state       string
		message     string
		expectWrite bool
		expectErr   bool
	}{
		{
			name:        "no existing status for this context: writes",
			existing:    map[string]*github.RepoStatus{},
			state:       "pending",
			message:     "Terraform plan: pending",
			expectWrite: true,
		},
		{
			name: "identical state, description, and no target URL: skips write",
			existing: map[string]*github.RepoStatus{
				"TFE/acme/prod": {
					State:       github.String("pending"),
					Description: github.String("Terraform plan: pending"),
				},
			},
			state:       "pending",
			message:     "Terraform plan: pending",
			expectWrite: false,
		},
		{
			name: "state differs: writes",
			existing: map[string]*github.RepoStatus{
				"TFE/acme/prod": {
					State:       github.String("pending"),
					Description: github.String("Terraform plan: pending"),
				},
			},
			state:       "success",
			message:     "Terraform plan: pending",
			expectWrite: true,
		},
		{
			name: "description differs: writes",
			existing: map[string]*github.RepoStatus{
				"TFE/acme/prod": {
					State:       github.String("pending"),
					Description: github.String("Terraform plan: pending"),
				},
			},
			state:       "pending",
			message:     "Terraform plan: 1 to add, 0 to change, 0 to destroy.",
			expectWrite: true,
		},
		{
			name: "same state/description but a new run ID changes the target URL: writes",
			existing: map[string]*github.RepoStatus{
				"TFE/acme/prod": {
					State:       github.String("pending"),
					Description: github.String("Terraform plan: pending"),
					TargetURL:   github.String("https://tfe.example.com/app/acme/workspaces/prod/runs/run-OLD"),
				},
			},
			runID:       "run-NEW",
			state:       "pending",
			message:     "Terraform plan: pending",
			expectWrite: true,
		},
		{
			name: "identical state/description/target URL for the same run: skips write",
			existing: map[string]*github.RepoStatus{
				"TFE/acme/prod": {
					State:       github.String("pending"),
					Description: github.String("Terraform plan: pending"),
					TargetURL:   github.String("https://tfe.example.com/app/acme/workspaces/prod/runs/run-1"),
				},
			},
			runID:       "run-1",
			state:       "pending",
			message:     "Terraform plan: pending",
			expectWrite: false,
		},
		{
			name:        "an unrelated context in the existing statuses map is ignored: writes",
			existing:    map[string]*github.RepoStatus{"TFE/acme/other-workspace": {State: github.String("pending")}},
			state:       "pending",
			message:     "Terraform plan: pending",
			expectWrite: true,
		},
		{
			name:        "LatestDetailedStatuses errors: does not write, returns error",
			existingErr: errors.New("boom"),
			state:       "pending",
			message:     "Terraform plan: pending",
			expectWrite: false,
			expectErr:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client, called, closeFn := newTestGithubClient(t)
			defer closeFn()

			prctx := &fakePullContext{
				owner:       "acme-org",
				repo:        "repo",
				sha:         "deadbeef",
				statuses:    tc.existing,
				statusesErr: tc.existingErr,
			}

			b := &Base{
				PullOpts:          &PullEvaluationOptions{StatusCheckContext: "TFE"},
				TFEClientProvider: tp,
			}

			err := b.PostStatus(context.Background(), prctx, wkcfg, tc.runID, client, tc.state, tc.message)

			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.expectWrite, *called, "unexpected CreateStatus call state")
		})
	}
}
