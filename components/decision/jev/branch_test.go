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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/decision"
)

type stubDecider struct {
	answer *decision.Answer
	err    error
}

func (s *stubDecider) Decide(ctx context.Context, state any, questions map[string]decision.Question, opts ...decision.Option) (map[string]*decision.Answer, error) {
	if s.err != nil {
		return nil, s.err
	}
	return map[string]*decision.Answer{branchQuestionID: s.answer}, nil
}

func confidenceOf(v float64) *float64 { return &v }

func branchTestSetup(t *testing.T, answer *decision.Answer) func(ctx context.Context, input string) (string, error) {
	t.Helper()

	q, err := decision.NewChoiceQuestion("route the ticket", map[string]string{
		"billing":  "billing team",
		"shipping": "shipping team",
	})
	require.NoError(t, err)

	return NewChoiceBranchCondition(
		&stubDecider{answer: answer},
		func(ctx context.Context, input string) (any, error) { return input, nil },
		q,
		map[string]string{"billing": "billing_node", "shipping": "shipping_node"},
		0.8,
		"fallback_node",
	)
}

func TestChoiceBranchConditionRoutes(t *testing.T) {
	condition := branchTestSetup(t, &decision.Answer{
		Type:       decision.QuestionTypeChoice,
		Choice:     "billing",
		Confidence: confidenceOf(0.9),
	})

	node, err := condition(context.Background(), "ticket")
	require.NoError(t, err)
	assert.Equal(t, "billing_node", node)
}

func TestChoiceBranchConditionLowConfidenceFallback(t *testing.T) {
	condition := branchTestSetup(t, &decision.Answer{
		Type:       decision.QuestionTypeChoice,
		Choice:     "billing",
		Confidence: confidenceOf(0.5),
	})

	node, err := condition(context.Background(), "ticket")
	require.NoError(t, err)
	assert.Equal(t, "fallback_node", node)
}

func TestChoiceBranchConditionUnknownChoiceFallback(t *testing.T) {
	condition := branchTestSetup(t, &decision.Answer{
		Type:       decision.QuestionTypeChoice,
		Choice:     "legal",
		Confidence: confidenceOf(1),
	})

	node, err := condition(context.Background(), "ticket")
	require.NoError(t, err)
	assert.Equal(t, "fallback_node", node)
}

func TestChoiceBranchConditionMissingConfidenceFallback(t *testing.T) {
	condition := branchTestSetup(t, &decision.Answer{
		Type:   decision.QuestionTypeChoice,
		Choice: "billing",
	})

	node, err := condition(context.Background(), "ticket")
	require.NoError(t, err)
	assert.Equal(t, "fallback_node", node)
}

func TestChoiceBranchConditionNoFallback(t *testing.T) {
	q, err := decision.NewChoiceQuestion("route the ticket", map[string]string{"billing": "billing team"})
	require.NoError(t, err)

	condition := NewChoiceBranchCondition(
		&stubDecider{answer: &decision.Answer{
			Type:       decision.QuestionTypeChoice,
			Choice:     "unknown",
			Confidence: confidenceOf(1),
		}},
		func(ctx context.Context, input string) (any, error) { return input, nil },
		q,
		map[string]string{"billing": "billing_node"},
		0.8,
		"",
	)

	_, err = condition(context.Background(), "ticket")
	require.Error(t, err)
}

func TestChoiceBranchConditionNonChoiceQuestion(t *testing.T) {
	q, err := decision.NewScoreQuestion("rate", []string{"low", "high"})
	require.NoError(t, err)

	condition := NewChoiceBranchCondition(
		&stubDecider{},
		func(ctx context.Context, input string) (any, error) { return input, nil },
		q,
		map[string]string{"billing": "billing_node"},
		0.8,
		"fallback_node",
	)

	_, err = condition(context.Background(), "ticket")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be of type")
}
