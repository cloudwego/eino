/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package jev

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/decision"
)

const branchQuestionID = "branch"

// NewChoiceBranchCondition creates a branch condition that routes execution to
// a downstream node based on the answer of a choice question.
//
// stateOf converts the branch input into the state evaluated by the decider.
// routes maps each possible choice value to a downstream node key. Answers
// whose confidence is below minConfidence, or whose choice has no route, fall
// back to the fallback node; an empty fallback turns those cases into errors.
//
// The returned condition is compatible with compose.NewGraphBranch:
//
//	condition := jev.NewChoiceBranchCondition(decider, stateOf, question, routes, 0.8, "human_review")
//	graph.AddBranch("upstream_node", compose.NewGraphBranch(condition, endNodes))
func NewChoiceBranchCondition[I any](
	d decision.Decider,
	stateOf func(ctx context.Context, input I) (any, error),
	q decision.Question,
	routes map[string]string,
	minConfidence float64,
	fallback string,
	opts ...decision.Option,
) func(ctx context.Context, input I) (string, error) {
	return func(ctx context.Context, input I) (string, error) {
		if q.Type != decision.QuestionTypeChoice {
			return "", fmt.Errorf("jev: branch question must be of type %q, got %q", decision.QuestionTypeChoice, q.Type)
		}
		if len(routes) == 0 {
			return "", fmt.Errorf("jev: branch question requires at least one route")
		}

		state, err := stateOf(ctx, input)
		if err != nil {
			return "", fmt.Errorf("jev: build branch state: %w", err)
		}

		answers, err := d.Decide(ctx, state, map[string]decision.Question{branchQuestionID: q}, opts...)
		if err != nil {
			return "", err
		}

		answer, ok := answers[branchQuestionID]
		if !ok || answer == nil {
			return "", fmt.Errorf("jev: decider returned no answer for the branch question")
		}
		if answer.Type != decision.QuestionTypeChoice {
			return "", fmt.Errorf("jev: branch answer must be of type %q, got %q", decision.QuestionTypeChoice, answer.Type)
		}

		if answer.IsConfident(minConfidence) {
			if node, ok := routes[answer.Choice]; ok {
				return node, nil
			}
		}

		if fallback == "" {
			return "", fmt.Errorf("jev: branch answer %q is below the confidence threshold or has no route, and no fallback is set", answer.Choice)
		}
		return fallback, nil
	}
}
