package plan

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-github/v53/github"
	"github.com/palantir/policy-bot/policy"
	"github.com/palantir/policy-bot/pull"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

type MockPullContext struct {
	defaultBranch     string
	changedFiles      []*pull.File
	changedFilesError error
}

func (mpc *MockPullContext) DefaultBranch() string {
	return mpc.defaultBranch
}

func (mpc *MockPullContext) DownloadCode() (path string, cleanup func(), err error) {
	return ".", func() {}, nil
}

func (mpc *MockPullContext) LatestDetailedStatuses() (map[string]*github.RepoStatus, error) {
	statuses := map[string]*github.RepoStatus{}
	return statuses, nil
}

func (mpc *MockPullContext) IsTeamMember(team, user string) (bool, error) {
	return true, nil
}

func (mpc *MockPullContext) IsOrgMember(org, user string) (bool, error) {
	return true, nil
}

func (mpc *MockPullContext) TeamMembers(team string) ([]string, error) {
	return []string{}, nil
}

func (mpc *MockPullContext) OrganizationMembers(org string) ([]string, error) {
	return []string{}, nil
}

func (mpc *MockPullContext) EvaluationTimestamp() time.Time {
	return time.Now()
}

func (mpc *MockPullContext) RepositoryOwner() string {
	return "owner"
}

// RepositoryName returns the repo that the pull request targets.
func (mpc *MockPullContext) RepositoryName() string {
	return "fake_repo"
}

// Number returns the number of the pull request.
func (mpc *MockPullContext) Number() int {
	return 1
}

// Title returns the title of the pull request
func (mpc *MockPullContext) Title() string {
	return "title"
}

// Body returns a struct that includes LastEditedAt for the pull request body
func (mpc *MockPullContext) Body() (*pull.Body, error) {
	return &pull.Body{}, nil
}

// Author returns the username of the user who opened the pull request.
func (mpc *MockPullContext) Author() string {
	return "author"
}

// CreatedAt returns the time when the pull request was created.
func (mpc *MockPullContext) CreatedAt() time.Time {
	return time.Now()
}

// IsOpen returns true when the state of the pull request is "open"
func (mpc *MockPullContext) IsOpen() bool {
	return true
}

// IsClosed returns true when the state of the pull request is "closed"
func (mpc *MockPullContext) IsClosed() bool {
	return false
}

// HeadSHA returns the SHA of the head commit of the pull request.
func (mpc *MockPullContext) HeadSHA() string {
	return "abcd"
}

// Branches returns the base (also known as target) and head branch names
// of this pull request. Branches in this repository have no prefix, while
// branches in forks are prefixed with the owner of the fork and a colon.
// The base branch will always be unprefixed.
func (mpc *MockPullContext) Branches() (base string, head string) {
	return "main", "head"
}

// ChangedFiles returns the files that were changed in this pull request.
func (mpc *MockPullContext) ChangedFiles() ([]*pull.File, error) {
	return mpc.changedFiles, mpc.changedFilesError
}

// Commits returns the commits that are part of this pull request. The
// commit order is implementation dependent.
func (mpc *MockPullContext) Commits() ([]*pull.Commit, error) {
	return []*pull.Commit{}, nil
}

// PushedAt returns the time at which the commit with sha was pushed. The
// returned time may be after the actual push time, but must not be before.
func (mpc *MockPullContext) PushedAt(sha string) (time.Time, error) {
	return time.Now(), nil
}

// Comments lists all comments on a Pull Request. The comment order is
// implementation dependent.
func (mpc *MockPullContext) Comments() ([]*pull.Comment, error) {
	return []*pull.Comment{}, nil
}

// Reviews lists all reviews on a Pull Request. The review order is
// implementation dependent.
func (mpc *MockPullContext) Reviews() ([]*pull.Review, error) {
	return []*pull.Review{}, nil
}

// IsDraft returns the draft status of the Pull Request.
func (mpc *MockPullContext) IsDraft() bool {
	return false
}

// RepositoryCollaborators returns the repository collaborators.
func (mpc *MockPullContext) RepositoryCollaborators() ([]*pull.Collaborator, error) {
	return []*pull.Collaborator{}, nil
}

// CollaboratorPermission returns the permission level of user on the repository.
func (mpc *MockPullContext) CollaboratorPermission(user string) (pull.Permission, error) {
	return 1, nil
}

// Teams lists the set of team collaborators, along with their respective
// permission on a repo.
func (mpc *MockPullContext) Teams() (map[string]pull.Permission, error) {
	permissions := map[string]pull.Permission{}
	return permissions, nil
}

// RequestedReviewers returns any current and dismissed review requests on
// the pull request.
func (mpc *MockPullContext) RequestedReviewers() ([]*pull.Reviewer, error) {
	return []*pull.Reviewer{}, nil
}

// LatestStatuses returns a map of status check names to the latest result
func (mpc *MockPullContext) LatestStatuses() (map[string]string, error) {
	statuses := map[string]string{}
	return statuses, nil
}

// Labels returns a list of labels applied on the Pull Request
func (mpc *MockPullContext) Labels() ([]string, error) {
	return []string{}, nil
}

func TestMatchPR(t *testing.T) {
	testCases := []struct {
		Name                string
		WorkingDirectory    string
		TriggerPrefixes     []string
		SkipTriggerPrefixes bool
		PullContext         MockPullContext
		TargetBranch        string
		ExpectedMatch       bool
		ExpectedError       error
	}{
		{
			"Match '.'",
			".",
			[]string{},
			false,
			MockPullContext{
				defaultBranch: "test",
				changedFiles:  []*pull.File{},
			},
			"main",
			true,
			nil,
		},
		{
			"Match './test'",
			"test",
			[]string{},
			false,
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles: []*pull.File{
					{
						Filename:  "test/file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
				},
			},
			"main",
			true,
			nil,
		},
		{
			"No match './test'",
			"test",
			[]string{},
			false,
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles: []*pull.File{
					{
						Filename:  "alpha/file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
				},
			},
			"main",
			false,
			nil,
		},
		{
			"Match './alpha' in common workspace directories",
			"test",
			[]string{"alpha"},
			false,
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles: []*pull.File{
					{
						Filename:  "alpha/file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
				},
			},
			"main",
			true,
			nil,
		},
		{
			"Match './alpha' in common workspace directories, but skipped",
			"test",
			[]string{"alpha"},
			true, // skip common directories
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles: []*pull.File{
					{
						Filename:  "alpha/file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
				},
			},
			"main",
			false,
			nil,
		},
		{
			"No match './bravo' in common workspace directories",
			"test",
			[]string{"bravo"},
			true, // skip common directories
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles: []*pull.File{
					{
						Filename:  "alpha/file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
				},
			},
			"main",
			false,
			nil,
		},
		{
			"No match with several files",
			"test",
			[]string{"alpha", "bravo", "charlie"},
			true, // skip common directories
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles: []*pull.File{
					{
						Filename:  "alpha/file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
					{
						Filename:  "src/another_file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
					{
						Filename:  "src/alpha/bravo.ccp",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
				},
			},
			"main",
			false,
			nil,
		},
		{
			"No files",
			"test",
			[]string{"bravo"},
			true, // skip common directories
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles:  []*pull.File{},
			},
			"main",
			false,
			nil,
		},
		{
			"Matches './test', but not targeting relevant branch",
			"test",
			[]string{},
			false,
			MockPullContext{
				defaultBranch: "test_branch",
				changedFiles: []*pull.File{
					{
						Filename:  "test/file.go",
						Status:    0,
						Additions: 5,
						Deletions: 5,
					},
				},
			},
			"develop",
			false,
			nil,
		},
		{
			"changed files errors out",
			"test",
			[]string{},
			false,
			MockPullContext{
				defaultBranch:     "test_branch",
				changedFiles:      []*pull.File{},
				changedFilesError: errors.New("oops"),
			},
			"main",
			false,
			errors.New("oops"),
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Test Case: %s", tc.Name), func(t *testing.T) {
			cfg := &Config{
				TriggerPrefixes: tc.TriggerPrefixes,
			}
			wkCfg := WorkspaceConfig{
				Organization:        "test_org",
				Name:                "test_name",
				WorkingDirectory:    tc.WorkingDirectory,
				Branch:              tc.TargetBranch,
				Policy:              policy.Policy{},
				ShowSkipped:         false,
				SkipTriggerPrefixes: tc.SkipTriggerPrefixes,
			}
			ghClient := &github.Client{}
			plCtx := Context{
				ctx:             context.Background(),
				wkcfg:           wkCfg,
				triggerPrefixes: cfg.TriggerPrefixes,
				prctx:           &tc.PullContext,
				ghClient:        ghClient,
				tfeClient:       nil,
			}

			matched, err := plCtx.matchPR()

			if tc.ExpectedError == nil {
				assert.Nil(t, err)
			} else {
				assert.Error(t, err)
			}

			assert.Equal(t, tc.ExpectedMatch, matched)
		})
	}
}
