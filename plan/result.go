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

import "github.com/palantir/policy-bot/policy/common"

type EvaluationStatus int

const (
	StatusSkipped EvaluationStatus = iota // note: values used for ordering
	StatusPolicyPending
	StatusPolicyDisapproved
	StatusPlanCreated
	StatusPlanPending
	StatusPlanDone
)

func (s EvaluationStatus) String() string {
	switch s {
	case StatusSkipped:
		return "skipped"
	case StatusPolicyPending:
		return "policy_pending"
	case StatusPolicyDisapproved:
		return "policy_disapproved"
	case StatusPlanCreated:
		return "plan_created"
	case StatusPlanPending:
		return "plan_pending"
	case StatusPlanDone:
		return "plan_done"
	}
	return "unknown"
}

type Result struct {
	StatusDescription string
	Status            EvaluationStatus

	Error error

	PolicyResult common.Result

	RunID string
}
