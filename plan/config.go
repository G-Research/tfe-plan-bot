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
	"github.com/pkg/errors"

	"github.com/palantir/policy-bot/policy"
	"github.com/palantir/policy-bot/policy/approval"
)

type Config struct {
	Workspaces    []WorkspaceConfig `yaml:"workspaces"`
	ApprovalRules []*approval.Rule  `yaml:"approval_rules"`
	Comments      []Comment         `yaml:"comments"`
	// If changed files within a PR match any of these prefixes/directories,
	// then all relevant workspaces will be matched.
	TriggerPrefixes []string `yaml:"trigger_prefixes"`
	commentsParsed  bool
}

type WorkspaceConfig struct {
	Organization     string        `yaml:"organization"`
	Name             string        `yaml:"name"`
	WorkingDirectory string        `yaml:"working_directory"`
	Branch           string        `yaml:"branch"`
	Policy           policy.Policy `yaml:"policy"`
	Comment          string        `yaml:"comment"`
	ShowSkipped      bool          `yaml:"show_skipped"`
	// Allows the exclusion of this workspace from being considered when
	// evaluating Config.TriggerPrefixes
	SkipTriggerPrefixes bool `yaml:"skip_trigger_prefixes"`
}

type Comment struct {
	Name    string `yaml:"name"`
	Content string `yaml:"content"`
}

func (w WorkspaceConfig) String() string {
	return w.Organization + "/" + w.Name
}

func (c *Config) ParseComments() error {
	if c.commentsParsed {
		return nil
	}

	commentsByName := make(map[string]string)
	for _, comment := range c.Comments {
		commentsByName[comment.Name] = comment.Content
	}

	for i, w := range c.Workspaces {
		if w.Comment != "" {
			if comment, ok := commentsByName[w.Comment]; ok {
				c.Workspaces[i].Comment = comment
			} else {
				names := make([]string, 0, len(commentsByName))
				for n := range commentsByName {
					names = append(names, n)
				}
				return errors.Errorf("workspace references undefined comment %q, allowed values: %v", w.Comment, names)
			}
		}
	}

	c.commentsParsed = true

	return nil
}
