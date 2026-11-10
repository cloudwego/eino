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

package adk

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDesignateAgentIndependentOptions(t *testing.T) {
	base := AgentRunOption{agentNames: make([]string, 1, 4)}
	base.agentNames[0] = "shared"
	first := base.DesignateAgent("first")
	second := base.DesignateAgent("second")
	unchanged := base.DesignateAgent()
	third := unchanged.DesignateAgent("third")
	require.Equal(t, []string{"shared"}, base.agentNames)
	require.Equal(t, []string{"shared", "first"}, first.agentNames)
	require.Equal(t, []string{"shared", "second"}, second.agentNames)
	require.Equal(t, []string{"shared"}, unchanged.agentNames)
	require.Equal(t, []string{"shared", "third"}, third.agentNames)
}

func TestDesignateAgentConcurrentDerivation(t *testing.T) {
	base := AgentRunOption{agentNames: make([]string, 1, 4)}
	base.agentNames[0] = "shared"
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				derived := base.DesignateAgent(name)
				if derived.agentNames[1] != name {
					t.Errorf("designation changed: got %q, want %q", derived.agentNames[1], name)
				}
			}
		}(name)
	}
	wg.Wait()
}

type designatedOptionValue struct{ value string }

type designatedOptionAgent struct {
	name string
	got  string
}

func (a *designatedOptionAgent) Name(context.Context) string { return a.name }
func (a *designatedOptionAgent) Description(context.Context) string {
	return "records designated options"
}
func (a *designatedOptionAgent) Run(_ context.Context, _ *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	a.got = GetImplSpecificOptions[designatedOptionValue](nil, opts...).value
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	gen.Close()
	return iter
}

func TestDesignateAgentDerivedOptionsThroughRunner(t *testing.T) {
	ctx := context.Background()
	base := WrapImplSpecificOptFn(func(o *designatedOptionValue) { o.value = "configured" })
	base = base.DesignateAgent("shared1").DesignateAgent("shared2").DesignateAgent("shared3")
	first := base.DesignateAgent("first")
	second := base.DesignateAgent("second")
	for _, tc := range []struct {
		name string
		opt  AgentRunOption
		want string
	}{
		{"first", first, "configured"},
		{"second", first, ""},
		{"first", second, ""},
		{"second", second, "configured"},
		{"shared1", first, "configured"},
		{"shared1", second, "configured"},
	} {
		a := &designatedOptionAgent{name: tc.name}
		iter := NewRunner(ctx, RunnerConfig{Agent: a}).Query(ctx, "test", tc.opt)
		for event, ok := iter.Next(); ok; event, ok = iter.Next() {
			require.NoError(t, event.Err)
		}
		require.Equal(t, tc.want, a.got, "agent %s", tc.name)
	}
}
