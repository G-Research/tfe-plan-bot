package plan

import (
	"fmt"
	"testing"

	"github.com/palantir/policy-bot/pull"
	"github.com/stretchr/testify/assert"
)

func TestFilesMatchDirectory(t *testing.T) {
	tests := []struct {
		name  string
		files []*pull.File
		dir   string
		want  bool
	}{
		{
			name:  "match",
			files: []*pull.File{{Filename: "infra/main.tf"}},
			dir:   "infra",
			want:  true,
		},
		{
			name:  "no match",
			files: []*pull.File{{Filename: "src/app.go"}},
			dir:   "infra",
			want:  false,
		},
		{
			name:  "prefix but not directory boundary",
			files: []*pull.File{{Filename: "infrastructure/main.tf"}},
			dir:   "infra",
			want:  false,
		},
		{
			name:  "empty files",
			files: []*pull.File{},
			dir:   "infra",
			want:  false,
		},
		{
			name:  "nil file in list",
			files: []*pull.File{nil, {Filename: "infra/main.tf"}},
			dir:   "infra",
			want:  true,
		},
		{
			name:  "nested directory match",
			files: []*pull.File{{Filename: "modules/networking/vpc.tf"}},
			dir:   "modules/networking",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilesMatchDirectory(tt.files, tt.dir)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFilesMatchAnyDirectory(t *testing.T) {
	files := []*pull.File{
		{Filename: "modules/networking/vpc.tf"},
	}

	assert.True(t, FilesMatchAnyDirectory(files, []string{"other", "modules/networking"}))
	assert.False(t, FilesMatchAnyDirectory(files, []string{"infra", "src"}))
	assert.False(t, FilesMatchAnyDirectory(files, []string{}))
}

func TestFilterRelevantWorkspaces(t *testing.T) {
	testCases := []struct {
		name           string
		cfg            *Config
		baseBranch     string
		defaultBranch  string
		changedFiles   []*pull.File
		expectedNames  []string
	}{
		{
			name: "filters by branch, working dir, and trigger prefixes",
			cfg: &Config{
				TriggerPrefixes: []string{"modules/common"},
				Workspaces: []WorkspaceConfig{
					{Organization: "org", Name: "ws-infra", WorkingDirectory: "infra", Branch: "main"},
					{Organization: "org", Name: "ws-app", WorkingDirectory: "app", Branch: "main"},
					{Organization: "org", Name: "ws-wrong-branch", WorkingDirectory: "infra", Branch: "develop"},
					{Organization: "org", Name: "ws-root", WorkingDirectory: ".", Branch: "main"},
					{Organization: "org", Name: "ws-skip-triggers", WorkingDirectory: "other", Branch: "main", SkipTriggerPrefixes: true},
				},
			},
			baseBranch:    "main",
			defaultBranch: "main",
			changedFiles: []*pull.File{
				{Filename: "infra/main.tf"},
				{Filename: "modules/common/variables.tf"},
			},
			expectedNames: []string{"ws-infra", "ws-app", "ws-root"},
		},
		{
			name: "uses default branch when workspace branch is empty",
			cfg: &Config{
				Workspaces: []WorkspaceConfig{
					{Organization: "org", Name: "ws-default", WorkingDirectory: "infra", Branch: ""},
				},
			},
			baseBranch:    "main",
			defaultBranch: "main",
			changedFiles: []*pull.File{
				{Filename: "infra/main.tf"},
			},
			expectedNames: []string{"ws-default"},
		},
		{
			name: "no workspaces match",
			cfg: &Config{
				Workspaces: []WorkspaceConfig{
					{Organization: "org", Name: "ws-infra", WorkingDirectory: "infra", Branch: "main"},
				},
			},
			baseBranch:    "main",
			defaultBranch: "main",
			changedFiles: []*pull.File{
				{Filename: "docs/readme.md"},
			},
			expectedNames: []string{},
		},
		{
			name: "all workspaces on wrong branch",
			cfg: &Config{
				Workspaces: []WorkspaceConfig{
					{Organization: "org", Name: "ws1", WorkingDirectory: "infra", Branch: "release"},
					{Organization: "org", Name: "ws2", WorkingDirectory: "app", Branch: "release"},
				},
			},
			baseBranch:    "main",
			defaultBranch: "main",
			changedFiles: []*pull.File{
				{Filename: "infra/main.tf"},
			},
			expectedNames: []string{},
		},
		{
			name: "working directory dot matches all files",
			cfg: &Config{
				Workspaces: []WorkspaceConfig{
					{Organization: "org", Name: "ws-root", WorkingDirectory: ".", Branch: "main"},
					{Organization: "org", Name: "ws-root-empty", WorkingDirectory: "", Branch: "main"},
				},
			},
			baseBranch:    "main",
			defaultBranch: "main",
			changedFiles: []*pull.File{
				{Filename: "anything/at/all.txt"},
			},
			expectedNames: []string{"ws-root", "ws-root-empty"},
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Test Case: %s", tc.name), func(t *testing.T) {
			result := FilterRelevantWorkspaces(tc.cfg, tc.baseBranch, tc.defaultBranch, tc.changedFiles)

			resultNames := make([]string, len(result))
			for i, ws := range result {
				resultNames[i] = ws.Name
			}

			assert.ElementsMatch(t, tc.expectedNames, resultNames)
		})
	}
}
