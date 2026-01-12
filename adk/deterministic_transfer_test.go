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

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

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
				assert.Len(t, ev.RunPath, 2, "innerAgent resumeFn: RunPath should have 2 steps (outer agent, inner agent)")
				if len(ev.RunPath) == 2 {
					assert.Equal(t, "outer", ev.RunPath[0].agentName, "innerAgent resumeFn: RunPath[0] should be outer agent")
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
				assert.Equal(t, "inner", ev.AgentName, "outerAgent resumeFn: event should be from inner agent (preserved original)")
				assert.Equal(t, "before interrupt", ev.Output.MessageOutput.Message.Content, "outerAgent resumeFn: event content should be 'before interrupt'")
				assert.Len(t, ev.RunPath, 2, "outerAgent resumeFn: RunPath should have 2 steps")
				if len(ev.RunPath) == 2 {
					assert.Equal(t, "outer", ev.RunPath[0].agentName, "outerAgent resumeFn: RunPath[0] should be outer agent")
					assert.Equal(t, "inner", ev.RunPath[1].agentName, "outerAgent resumeFn: RunPath[1] should be inner agent")
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

		if ev.Err != nil {
			resumeErr = ev.Err
		}
		if ev.Action != nil && ev.Action.TransferToAgent != nil {
			hasTransfer = true
		}
		resumeEvents = append(resumeEvents, ev)
	}

	assert.Equal(t, 2, runCount, "inner agent should be called twice (once for initial, once for resume)")
	assert.NotEmpty(t, resumeEvents, "should have resume events")
	assert.True(t, hasTransfer, "should have transfer action after resume")
	assert.Error(t, resumeErr, "transfer should fail because next_agent doesn't exist")
	assert.Contains(t, resumeErr.Error(), "next_agent", "error should mention the missing agent")
}

func TestDeterministicTransferRunPathPreserved(t *testing.T) {
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

	assert.NotEmpty(t, collectedRunPaths, "should have collected RunPaths")
	for _, rp := range collectedRunPaths {
		assert.Len(t, rp, 2, "RunPath should have 2 steps (outer agent, inner agent)")
		if len(rp) == 2 {
			assert.Equal(t, "outer", rp[0].agentName, "RunPath[0] should be outer agent")
			assert.Equal(t, "inner", rp[1].agentName, "RunPath[1] should be inner agent")
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

type nonFlowTestAgent struct {
	name     string
	runFn    func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
	resumeFn func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent]
}

func (a *nonFlowTestAgent) Name(_ context.Context) string {
	return a.name
}

func (a *nonFlowTestAgent) Description(_ context.Context) string {
	return a.name + " description"
}

func (a *nonFlowTestAgent) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return a.runFn(ctx, input, options...)
}

func (a *nonFlowTestAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if a.resumeFn != nil {
		return a.resumeFn(ctx, info, opts...)
	}
	return a.runFn(ctx, &AgentInput{}, opts...)
}

type nonResumableTestAgent struct {
	name  string
	runFn func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
}

func (a *nonResumableTestAgent) Name(_ context.Context) string {
	return a.name
}

func (a *nonResumableTestAgent) Description(_ context.Context) string {
	return a.name + " description"
}

func (a *nonResumableTestAgent) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return a.runFn(ctx, input, options...)
}

func TestDeterministicTransferNonFlowAgent_ExitSkipsTransfer(t *testing.T) {
	ctx := context.Background()

	agent := &nonFlowTestAgent{
		name: "test_agent",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := EventFromMessage(schema.AssistantMessage("exiting", nil), nil, schema.Assistant, "")
				ev.Action = &AgentAction{Exit: true}
				gen.Send(ev)
			}()
			return iter
		},
	}

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        agent,
		ToAgentNames: []string{"next_agent"},
	})

	iter := wrapped.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})

	var events []*AgentEvent
	var sawExit bool
	var sawTransfer bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
		if ev.Action != nil && ev.Action.Exit {
			sawExit = true
		}
		if ev.Action != nil && ev.Action.TransferToAgent != nil {
			sawTransfer = true
		}
	}

	assert.True(t, sawExit, "should see exit event")
	assert.False(t, sawTransfer, "should NOT see transfer when exit is last event")
	assert.Len(t, events, 1, "should have exactly 1 event (exit)")
}

func TestDeterministicTransferNonFlowAgent_AppendsTransfer(t *testing.T) {
	ctx := context.Background()

	agent := &nonFlowTestAgent{
		name: "test_agent",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := EventFromMessage(schema.AssistantMessage("normal output", nil), nil, schema.Assistant, "")
				gen.Send(ev)
			}()
			return iter
		},
	}

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        agent,
		ToAgentNames: []string{"next_agent"},
	})

	iter := wrapped.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})

	var events []*AgentEvent
	var sawTransfer bool
	var transferTarget string
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
		if ev.Action != nil && ev.Action.TransferToAgent != nil {
			sawTransfer = true
			transferTarget = ev.Action.TransferToAgent.DestAgentName
		}
	}

	assert.True(t, sawTransfer, "should see transfer event after normal completion")
	assert.Equal(t, "next_agent", transferTarget, "transfer target should be next_agent")
	assert.Greater(t, len(events), 1, "should have more than 1 event (output + transfer messages)")
}

func TestDeterministicTransferNonFlowAgent_InterruptSkipsTransfer(t *testing.T) {
	ctx := context.Background()

	agent := &nonFlowTestAgent{
		name: "test_agent",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := &AgentEvent{
					Action: &AgentAction{
						Interrupted: &InterruptInfo{Data: "test interrupt"},
					},
				}
				gen.Send(ev)
			}()
			return iter
		},
	}

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        agent,
		ToAgentNames: []string{"next_agent"},
	})

	iter := wrapped.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})

	var events []*AgentEvent
	var sawInterrupt bool
	var sawTransfer bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
		if ev.Action != nil && ev.Action.Interrupted != nil {
			sawInterrupt = true
		}
		if ev.Action != nil && ev.Action.TransferToAgent != nil {
			sawTransfer = true
		}
	}

	assert.True(t, sawInterrupt, "should see interrupt event")
	assert.False(t, sawTransfer, "should NOT see transfer when interrupted")
	assert.Len(t, events, 1, "should have exactly 1 event (interrupt)")
}

func TestDeterministicTransferNonFlowAgent_Resume(t *testing.T) {
	ctx := context.Background()

	var resumeCalled bool
	agent := &nonFlowTestAgent{
		name: "test_agent",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := EventFromMessage(schema.AssistantMessage("from run", nil), nil, schema.Assistant, "")
				gen.Send(ev)
			}()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			resumeCalled = true
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := EventFromMessage(schema.AssistantMessage("from resume", nil), nil, schema.Assistant, "")
				gen.Send(ev)
			}()
			return iter
		},
	}

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        agent,
		ToAgentNames: []string{"next_agent"},
	})

	ra, ok := wrapped.(ResumableAgent)
	assert.True(t, ok, "wrapped agent should be ResumableAgent")

	iter := ra.Resume(ctx, &ResumeInfo{WasInterrupted: true})

	var events []*AgentEvent
	var sawTransfer bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
		if ev.Action != nil && ev.Action.TransferToAgent != nil {
			sawTransfer = true
		}
	}

	assert.True(t, resumeCalled, "resume should have been called on inner agent")
	assert.True(t, sawTransfer, "should see transfer event after resume completion")
}

func TestDeterministicTransferFlowAgent_ResumeWithInvalidState(t *testing.T) {
	ctx := context.Background()

	innerAgent := &dtTestAgent{
		name: "inner",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				gen.Send(EventFromMessage(schema.AssistantMessage("test", nil), nil, schema.Assistant, ""))
			}()
			return iter
		},
	}

	innerFlowAgent := toFlowAgent(ctx, innerAgent)

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        innerFlowAgent,
		ToAgentNames: []string{"next_agent"},
	})

	ra, ok := wrapped.(ResumableAgent)
	assert.True(t, ok, "wrapped flowAgent should be ResumableAgent")

	iter := ra.Resume(ctx, &ResumeInfo{
		WasInterrupted: true,
		InterruptState: nil,
	})

	var gotError bool
	var errorMsg string
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			gotError = true
			errorMsg = ev.Err.Error()
		}
	}

	assert.True(t, gotError, "should get error for invalid state")
	assert.Contains(t, errorMsg, "invalid interrupt state", "error should mention invalid state")
}

func TestDeterministicTransferNonResumableAgent(t *testing.T) {
	ctx := context.Background()

	agent := &nonResumableTestAgent{
		name: "non_resumable",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				ev := EventFromMessage(schema.AssistantMessage("output", nil), nil, schema.Assistant, "")
				gen.Send(ev)
			}()
			return iter
		},
	}

	wrapped := AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
		Agent:        agent,
		ToAgentNames: []string{"next_agent"},
	})

	_, isResumable := wrapped.(ResumableAgent)
	assert.False(t, isResumable, "wrapped non-resumable agent should NOT be ResumableAgent")

	assert.Equal(t, "non_resumable", wrapped.Name(ctx), "Name should delegate to inner agent")
	assert.Equal(t, "non_resumable description", wrapped.Description(ctx), "Description should delegate to inner agent")

	iter := wrapped.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("test")}})

	var sawTransfer bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.TransferToAgent != nil {
			sawTransfer = true
		}
	}

	assert.True(t, sawTransfer, "should see transfer event")
}
