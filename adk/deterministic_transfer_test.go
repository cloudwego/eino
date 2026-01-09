/*
 * Copyright 2025 CloudWeGo Authors
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
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestStripCurrentRunnerScope(t *testing.T) {
	tests := []struct {
		name     string
		input    []RunStep
		expected []RunStep
	}{
		{
			name:     "empty path",
			input:    nil,
			expected: nil,
		},
		{
			name:     "single runner scope - cleared",
			input:    []RunStep{{runnerName: "inner"}, {agentName: "innerAgent"}},
			expected: nil,
		},
		{
			name:     "runner step only - cleared",
			input:    []RunStep{{runnerName: "inner"}},
			expected: nil,
		},
		{
			name:     "nested runner scope - preserves nested",
			input:    []RunStep{{runnerName: "inner"}, {agentName: "innerAgent"}, {runnerName: "nested"}, {agentName: "nestedAgent"}},
			expected: []RunStep{{runnerName: "nested"}, {agentName: "nestedAgent"}},
		},
		{
			name:     "multiple agent steps before nested runner",
			input:    []RunStep{{runnerName: "inner"}, {agentName: "a1"}, {agentName: "a2"}, {runnerName: "nested"}, {agentName: "nestedAgent"}},
			expected: []RunStep{{runnerName: "nested"}, {agentName: "nestedAgent"}},
		},
		{
			name:     "deeply nested - preserves all nested scopes",
			input:    []RunStep{{runnerName: "r1"}, {agentName: "a1"}, {runnerName: "r2"}, {agentName: "a2"}, {runnerName: "r3"}, {agentName: "a3"}},
			expected: []RunStep{{runnerName: "r2"}, {agentName: "a2"}, {runnerName: "r3"}, {agentName: "a3"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripCurrentRunnerScope(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

type dtTestStore struct {
	data map[string][]byte
}

func newDTTestStore() *dtTestStore {
	return &dtTestStore{data: make(map[string][]byte)}
}

func (s *dtTestStore) Set(_ context.Context, key string, value []byte) error {
	s.data[key] = value
	return nil
}

func (s *dtTestStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.data[key]
	return v, ok, nil
}

type dtTestAgent struct {
	name     string
	runFn    func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
	resumeFn func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent]
}

func (a *dtTestAgent) Name(_ context.Context) string {
	return a.name
}

func (a *dtTestAgent) Description(_ context.Context) string {
	return a.name + " description"
}

func (a *dtTestAgent) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return a.runFn(ctx, input, options...)
}

func (a *dtTestAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if a.resumeFn != nil {
		return a.resumeFn(ctx, info, opts...)
	}
	return a.runFn(ctx, &AgentInput{}, opts...)
}

func TestDeterministicTransferFlowAgentInterruptResume(t *testing.T) {
	ctx := context.Background()
	store := newDTTestStore()

	interruptData := "interrupt_data"
	var runCount int

	innerAgent := &dtTestAgent{
		name: "inner",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			runCount++
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				gen.Send(EventFromMessage(schema.AssistantMessage("before interrupt", nil), nil, schema.Assistant, ""))
				intEvent := Interrupt(ctx, interruptData)
				intEvent.Action.Interrupted.Data = interruptData
				gen.Send(intEvent)
			}()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			runCount++

			assert.True(t, info.WasInterrupted, "innerAgent resumeFn: should be interrupted")
			assert.True(t, info.IsResumeTarget, "innerAgent resumeFn: should be resume target")

			runCtx := getRunCtx(ctx)
			assert.NotNil(t, runCtx, "innerAgent resumeFn: runCtx should not be nil")
			assert.NotNil(t, runCtx.Session, "innerAgent resumeFn: runCtx.Session should not be nil")

			var agentEvents []*AgentEvent
			for _, ev := range runCtx.Session.Events {
				if ev.AgentEvent != nil {
					agentEvents = append(agentEvents, ev.AgentEvent)
				}
			}

			assert.Len(t, agentEvents, 1, "innerAgent resumeFn: should have exactly 1 agent event")
			if len(agentEvents) == 1 {
				ev := agentEvents[0]
				assert.Equal(t, "inner", ev.AgentName, "innerAgent resumeFn: event should be from inner agent")
				assert.Equal(t, "before interrupt", ev.Output.MessageOutput.Message.Content, "innerAgent resumeFn: event content should be 'before interrupt'")
				assert.Len(t, ev.RunPath, 2, "innerAgent resumeFn: RunPath should have 2 steps")
				if len(ev.RunPath) == 2 {
					assert.Equal(t, "inner", ev.RunPath[0].runnerName, "innerAgent resumeFn: RunPath[0] should be inner runner")
					assert.Equal(t, "inner", ev.RunPath[1].agentName, "innerAgent resumeFn: RunPath[1] should be inner agent")
				}
			}

			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				gen.Send(EventFromMessage(schema.AssistantMessage("after resume", nil), nil, schema.Assistant, ""))
			}()
			return iter
		},
	}

	innerFlowAgent := toFlowAgent(ctx, innerAgent)

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        innerFlowAgent,
		ToAgentNames: []string{"next_agent"},
	})

	outerAgent := &dtTestAgent{
		name: "outer",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			return wrapped.Run(ctx, input, options...)
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.True(t, info.WasInterrupted, "outerAgent resumeFn: should be interrupted")

			runCtx := getRunCtx(ctx)
			assert.NotNil(t, runCtx, "outerAgent resumeFn: runCtx should not be nil")
			assert.NotNil(t, runCtx.Session, "outerAgent resumeFn: runCtx.Session should not be nil")

			var agentEvents []*AgentEvent
			for _, ev := range runCtx.Session.Events {
				if ev.AgentEvent != nil {
					agentEvents = append(agentEvents, ev.AgentEvent)
				}
			}

			assert.Len(t, agentEvents, 1, "outerAgent resumeFn: should have exactly 1 agent event")
			if len(agentEvents) == 1 {
				ev := agentEvents[0]
				assert.Equal(t, "outer", ev.AgentName, "outerAgent resumeFn: event should be from outer agent")
				assert.Equal(t, "before interrupt", ev.Output.MessageOutput.Message.Content, "outerAgent resumeFn: event content should be 'before interrupt'")
				assert.Len(t, ev.RunPath, 2, "outerAgent resumeFn: RunPath should have 2 steps")
				if len(ev.RunPath) == 2 {
					assert.Equal(t, "outer", ev.RunPath[0].runnerName, "outerAgent resumeFn: RunPath[0] should be outer runner")
					assert.Equal(t, "outer", ev.RunPath[1].agentName, "outerAgent resumeFn: RunPath[1] should be outer agent")
				}
			}

			ra := wrapped.(ResumableAgent)
			return ra.Resume(ctx, info, opts...)
		},
	}

	outerFlowAgent := toFlowAgent(ctx, outerAgent)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           outerFlowAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, WithCheckPointID("cp1"))

	var events []*AgentEvent
	var interrupted bool
	var interruptEvent *AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
		m, _ := sonic.MarshalIndent(ev, "", "  ")
		t.Logf("Run Event: %s", string(m))
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interrupted = true
			interruptEvent = ev
		}
	}

	assert.Equal(t, 1, runCount, "run should have been called once")
	assert.True(t, interrupted, "should have interrupted")
	assert.Greater(t, len(events), 0, "should have events")
	if interruptEvent == nil {
		t.Fatal("should have interrupt event")
	}
	assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts, "should have interrupt contexts")

	_, exists, err := store.Get(ctx, "cp1")
	assert.NoError(t, err)
	assert.True(t, exists, "checkpoint should have been saved")

	var hasDeterministicTransferContext bool
	for _, intCtx := range interruptEvent.Action.Interrupted.InterruptContexts {
		t.Logf("InterruptContext: ID=%s, Info=%v, IsRootCause=%v, Addr=%v", intCtx.ID, intCtx.Info, intCtx.IsRootCause, intCtx.Address)
		if intCtx.Info == "deterministic transfer wrapper interrupted" {
			hasDeterministicTransferContext = true
		}
		for parent := intCtx.Parent; parent != nil; parent = parent.Parent {
			t.Logf("  Parent: ID=%s, Info=%v, Addr=%v", parent.ID, parent.Info, parent.Address)
			if parent.Info == "deterministic transfer wrapper interrupted" {
				hasDeterministicTransferContext = true
			}
		}
	}
	assert.True(t, hasDeterministicTransferContext, "should have deterministic transfer interrupt context")

	var rootCauseID string
	for _, intCtx := range interruptEvent.Action.Interrupted.InterruptContexts {
		if intCtx.IsRootCause {
			rootCauseID = intCtx.ID
			break
		}
	}
	assert.NotEmpty(t, rootCauseID, "should have root cause interrupt ID")

	resumeIter, err := runner.ResumeWithParams(ctx, "cp1", &ResumeParams{
		Targets: map[string]any{rootCauseID: nil},
	})
	assert.NoError(t, err)

	var resumeEvents []*AgentEvent
	var resumeErr error
	var hasTransfer bool
	for {
		ev, ok := resumeIter.Next()
		if !ok {
			break
		}

		m, _ := sonic.MarshalIndent(ev, "", "  ")
		t.Logf("Resume Event: %s", string(m))

		if ev.Err != nil {
			resumeErr = ev.Err
			t.Logf("Resume error: %v", resumeErr)
		}
		if ev.Action != nil && ev.Action.TransferToAgent != nil {
			hasTransfer = true
			t.Logf("Transfer to: %s", ev.Action.TransferToAgent.DestAgentName)
		}
		resumeEvents = append(resumeEvents, ev)
	}

	assert.Equal(t, 2, runCount, "inner agent should be called twice (once for initial, once for resume)")
	assert.NotEmpty(t, resumeEvents, "should have resume events")
	assert.True(t, hasTransfer, "should have transfer action after resume")
	assert.Error(t, resumeErr, "transfer should fail because next_agent doesn't exist")
	assert.Contains(t, resumeErr.Error(), "next_agent", "error should mention the missing agent")
}

func TestDeterministicTransferRunPathStripping(t *testing.T) {
	ctx := context.Background()
	store := newDTTestStore()

	var collectedRunPaths [][]RunStep

	innerAgent := &dtTestAgent{
		name: "inner",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := EventFromMessage(schema.AssistantMessage("from inner", nil), nil, schema.Assistant, "")
				gen.Send(ev)
			}()
			return iter
		},
	}

	innerFlowAgent := toFlowAgent(ctx, innerAgent)

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        innerFlowAgent,
		ToAgentNames: []string{},
	})

	outerAgent := &dtTestAgent{
		name: "outer",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			innerIter := wrapped.Run(ctx, input, options...)
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				for {
					ev, ok := innerIter.Next()
					if !ok {
						break
					}
					collectedRunPaths = append(collectedRunPaths, ev.RunPath)
					gen.Send(ev)
				}
			}()
			return iter
		},
	}

	outerFlowAgent := toFlowAgent(ctx, outerAgent)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           outerFlowAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, WithCheckPointID("cp1"))

	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	for _, rp := range collectedRunPaths {
		for _, step := range rp {
			if step.runnerName == "inner" {
				t.Errorf("inner runner scope should have been stripped, but found runnerName='inner' in RunPath: %+v", rp)
			}
		}
	}
}

func TestDeterministicTransferExitSkipsTransfer(t *testing.T) {
	ctx := context.Background()
	store := newDTTestStore()

	innerAgent := &dtTestAgent{
		name: "inner",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := EventFromMessage(schema.AssistantMessage("inner exits", nil), nil, schema.Assistant, "")
				ev.Action = &AgentAction{Exit: true}
				gen.Send(ev)
			}()
			return iter
		},
	}

	innerFlowAgent := toFlowAgent(ctx, innerAgent)

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        innerFlowAgent,
		ToAgentNames: []string{"next_agent"},
	})

	var outerSawExit bool
	var transferGenerated bool

	outerAgent := &dtTestAgent{
		name: "outer",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			innerIter := wrapped.Run(ctx, input, options...)
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				for {
					ev, ok := innerIter.Next()
					if !ok {
						break
					}
					if ev.Action != nil && ev.Action.Exit {
						outerSawExit = true
					}
					if ev.Action != nil && ev.Action.TransferToAgent != nil {
						transferGenerated = true
					}
					gen.Send(ev)
				}
			}()
			return iter
		},
	}

	outerFlowAgent := toFlowAgent(ctx, outerAgent)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           outerFlowAgent,
		EnableStreaming: true,
		CheckPointStore: store,
	})

	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, WithCheckPointID("cp1"))

	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	assert.True(t, outerSawExit, "outer should see exit event from inner")
	assert.False(t, transferGenerated, "transfer should not be generated when inner exits")
}
