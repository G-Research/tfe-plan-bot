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
	"github.com/google/go-github/v32/github"

	"github.com/palantir/policy-bot/pull"
)

type Context interface {
	pull.Context

	// DefaultBranch returns the default branch of the repo that the pull
	// request targets.
	DefaultBranch() string

	// Title returns the title of the pull request.
	Title() string

	// DownloadCode downloads the code to a temporary directory and returns its
	// path and a cleanup function.
	DownloadCode() (path string, cleanup func(), err error)

	// LatestDetailedStatuses returns a map of status check names to the latest
	// detailed result
	LatestDetailedStatuses() (map[string]*github.RepoStatus, error)
}
