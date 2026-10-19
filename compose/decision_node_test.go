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

package compose

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/decision"
)

type stubDecider struct{}

func (s *stubDecider) Decide(ctx context.Context, state any, questions map[string]decision.Question, opts ...decision.Option) (map[string]*decision.Answer, error) {
	confidence := 1.0
	answers := make(map[string]*decision.Answer, len(questions))
	for id := range questions {
		answers[id] = &decision.Answer{
			Type:       decision.QuestionTypeChoice,
			Choice:     "billing",
			Confidence: &confidence,
		}
	}
	return answers, nil
}

func decisionNodeInput(t *testing.T) *decision.NodeInput {
	t.Helper()

	q, err := decision.NewChoiceQuestion("route the ticket", map[string]string{"billing": "billing team"})
	require.NoError(t, err)

	return &decision.NodeInput{
		State:     "where is my invoice",
		Questions: map[string]decision.Question{"route": q},
	}
}

func TestDecisionNodeInChain(t *testing.T) {
	ctx := context.Background()

	chain := NewChain[*decision.NodeInput, map[string]*decision.Answer]()
	chain.AppendDecision(&stubDecider{})

	r, err := chain.Compile(ctx)
	require.NoError(t, err)

	var observedComponent components.Component
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			observedComponent = info.Component
			return ctx
		}).
		Build()

	out, err := r.Invoke(ctx, decisionNodeInput(t), WithCallbacks(handler))
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "billing", out["route"].Choice)
	assert.Equal(t, components.ComponentOfDecision, observedComponent)
}

func TestDecisionNodeInGraph(t *testing.T) {
	ctx := context.Background()

	g := NewGraph[*decision.NodeInput, map[string]*decision.Answer]()
	err := g.AddDecisionNode("decide", &stubDecider{})
	require.NoError(t, err)
	require.NoError(t, g.AddEdge(START, "decide"))
	require.NoError(t, g.AddEdge("decide", END))

	r, err := g.Compile(ctx)
	require.NoError(t, err)

	var observedComponent components.Component
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component == components.ComponentOfDecision {
				observedComponent = info.Component
			}
			return ctx
		}).
		Build()

	out, err := r.Invoke(ctx, decisionNodeInput(t), WithCallbacks(handler))
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "billing", out["route"].Choice)
	assert.Equal(t, components.ComponentOfDecision, observedComponent)
}

func TestDecisionNodeInWorkflow(t *testing.T) {
	ctx := context.Background()

	wf := NewWorkflow[*decision.NodeInput, map[string]*decision.Answer]()
	wf.AddDecisionNode("decide", &stubDecider{}).AddInput(START)
	wf.End().AddInput("decide")

	r, err := wf.Compile(ctx)
	require.NoError(t, err)

	out, err := r.Invoke(ctx, decisionNodeInput(t))
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Equal(t, "billing", out["route"].Choice)
}
