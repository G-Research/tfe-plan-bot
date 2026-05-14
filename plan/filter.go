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
	"path"
	"strings"

	"github.com/palantir/policy-bot/pull"
)

// FilterRelevantWorkspaces returns only the workspaces whose branch and file
// paths match the pull request, avoiding unnecessary evaluation of workspaces
// that would be skipped by matchPR. This allows callers to pre-filter before
// spawning concurrent evaluations.
func FilterRelevantWorkspaces(cfg *Config, baseBranch string, defaultBranch string, changedFiles []*pull.File) []WorkspaceConfig {
	relevant := make([]WorkspaceConfig, 0, len(cfg.Workspaces))

	for _, wkcfg := range cfg.Workspaces {
		// Determine expected branch
		branch := wkcfg.Branch
		if branch == "" {
			branch = defaultBranch
		}

		// Skip if PR doesn't target this workspace's branch
		if baseBranch != branch {
			continue
		}

		// Check if changed files match trigger prefixes
		if !wkcfg.SkipTriggerPrefixes {
			if FilesMatchAnyDirectory(changedFiles, cfg.TriggerPrefixes) {
				relevant = append(relevant, wkcfg)
				continue
			}
		}

		// Check working directory
		workingDir := path.Clean(wkcfg.WorkingDirectory)
		if workingDir == "." {
			relevant = append(relevant, wkcfg)
			continue
		}

		if FilesMatchDirectory(changedFiles, workingDir) {
			relevant = append(relevant, wkcfg)
		}
	}

	return relevant
}

// FilesMatchDirectory returns true if any of the given files have a path
// prefixed by dir + "/".
func FilesMatchDirectory(files []*pull.File, dir string) bool {
	prefix := dir + "/"
	for _, file := range files {
		if file != nil && strings.HasPrefix(file.Filename, prefix) {
			return true
		}
	}
	return false
}

// FilesMatchAnyDirectory returns true if any file matches any of the given
// directories.
func FilesMatchAnyDirectory(files []*pull.File, dirs []string) bool {
	for _, dir := range dirs {
		if FilesMatchDirectory(files, dir) {
			return true
		}
	}
	return false
}
