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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// approvalInfoSpan and approvalResultSpan are isolated copies for use in this
// test file so we don't conflict with the prebuilt/integration_test.go types
// (which live in a different package anyway).
type approvalInfoSpan struct {
	ToolName        string
	ArgumentsInJSON string
	ToolCallID      string
}

type approvalResultSpan struct {
	Approved bool
}

func init() {
	schema.Register[*approvalInfoSpan]()
	schema.Register[*approvalResultSpan]()
}

// approvableSpanTool is an invokable tool that interrupts on first invocation
// and runs to completion on resume after approval.
type approvableSpanTool struct {
	name string
}

func (t *approvableSpanTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "approvable span tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *approvableSpanTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	wasInterrupted, _, savedArgs := tool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return "", tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: argumentsInJSON,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, argumentsInJSON)
	}
	isResumeTarget, hasData, data := tool.GetResumeContext[*approvalResultSpan](ctx)
	if !isResumeTarget || !hasData {
		return "", tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: savedArgs,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, savedArgs)
	}
	if data.Approved {
		return fmt.Sprintf("Tool '%s' executed with args: %s", t.name, savedArgs), nil
	}
	return fmt.Sprintf("Tool '%s' rejected", t.name), nil
}

// approvableStreamableSpanTool: streamable variant. Interrupts on first
// invocation by returning a *core.InterruptSignal error before any stream
// chunk is produced; runs to completion on resume.
type approvableStreamableSpanTool struct {
	name string
}

func (t *approvableStreamableSpanTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "approvable streamable span tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *approvableStreamableSpanTool) StreamableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	wasInterrupted, _, savedArgs := tool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return nil, tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: argumentsInJSON,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, argumentsInJSON)
	}
	isResumeTarget, hasData, data := tool.GetResumeContext[*approvalResultSpan](ctx)
	if !isResumeTarget || !hasData {
		return nil, tool.StatefulInterrupt(ctx, &approvalInfoSpan{
			ToolName:        t.name,
			ArgumentsInJSON: savedArgs,
			ToolCallID:      compose.GetToolCallID(ctx),
		}, savedArgs)
	}
	if data.Approved {
		return schema.StreamReaderFromArray([]string{
			fmt.Sprintf("Tool '%s' streamed with args: %s", t.name, savedArgs),
		}), nil
	}
	return schema.StreamReaderFromArray([]string{"rejected"}), nil
}

// alwaysErrorTool errors out hard (non-interrupt) on every invocation.
type alwaysErrorTool struct {
	name string
}

func (t *alwaysErrorTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "always errors",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *alwaysErrorTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "", errors.New("hard tool failure")
}

// memCheckpointStore is a minimal in-memory CheckPointStore for these tests.
type memCheckpointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemCheckpointStore() *memCheckpointStore {
	return &memCheckpointStore{data: make(map[string][]byte)}
}

func (s *memCheckpointStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
	return nil
}

func (s *memCheckpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[key]
	return v, ok, nil
}

// scriptedToolCallingModel is a controllable mock model: each call returns the
// next scripted message.
type scriptedToolCallingModel struct {
	mu       sync.Mutex
	messages []*schema.Message
	pos      int
}

func (m *scriptedToolCallingModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pos >= len(m.messages) {
		return schema.AssistantMessage("done", nil), nil
	}
	msg := m.messages[m.pos]
	m.pos++
	return msg, nil
}

func (m *scriptedToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *scriptedToolCallingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

// drainAndCollectSpans collects tool span events emitted during iter draining.
func drainAndCollectSpans(t *testing.T, iter *AsyncIterator[*AgentEvent]) (starts, ends []*SessionEvent[*schema.Message], interrupted bool) {
	t.Helper()
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interrupted = true
		}
		if ev.SessionEvent == nil || ev.SessionEvent.Span == nil {
			continue
		}
		switch ev.SessionEvent.Kind {
		case SessionEventSpanToolCallStart:
			starts = append(starts, ev.SessionEvent)
		case SessionEventSpanToolCallEnd:
			ends = append(ends, ev.SessionEvent)
		}
	}
	return
}

// setupApprovableSpanAgent constructs a ChatModelAgent with a scripted
// tool-calling model and an in-memory checkpoint store, ready for span tests
// that exercise interrupt/resume.
func setupApprovableSpanAgent(t *testing.T, name string, tools []tool.BaseTool, scriptedAssistant []*schema.Message) (*TypedChatModelAgent[*schema.Message], *memCheckpointStore) {
	t.Helper()
	ctx := context.Background()
	mdl := &scriptedToolCallingModel{messages: scriptedAssistant}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        name,
		Description: "test",
		Model:       mdl,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	require.NoError(t, err)
	store := newMemCheckpointStore()
	return agent, store
}

func TestToolSpan_PermissionInterruptDefersEndSpan(t *testing.T) {
	ctx := context.Background()
	tl := &approvableSpanTool{name: "approve_me"}

	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call_1", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
	}
	agent, store := setupApprovableSpanAgent(t, "agent1", []tool.BaseTool{tl}, scripted)
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})

	checkpointID := "ckpt-1"
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())
	starts, ends, interrupted := drainAndCollectSpans(t, iter)

	assert.True(t, interrupted, "expected an interrupt event from the approvable tool")
	require.Len(t, starts, 1, "exactly one tool_call_start span should be emitted on the interrupted run")
	assert.Empty(t, ends, "no tool_call_end span should be emitted on the interrupted run")
	assert.Equal(t, "call_1", starts[0].Span.Tool.ToolUseID)
	assert.NotEmpty(t, starts[0].Span.Tool.AssistantMessageEventID, "start span must carry assistant message event ID")
	assert.NotEmpty(t, starts[0].Span.ParentSpanID, "start span must carry parent (model) span ID")
}

// runInterruptResumeAndCollectSpans drives a single-tool interrupt+approve
// scenario and returns the start span emitted on the original run plus the
// end span emitted on the resumed run. Used by both the resume happy-path
// test and the dedicated "parent IDs survive resume" assertion.
func runInterruptResumeAndCollectSpans(t *testing.T, agent *TypedChatModelAgent[*schema.Message], store *memCheckpointStore, checkpointID string) (startSpan, endSpan *SessionEvent[*schema.Message]) {
	t.Helper()
	ctx := context.Background()
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter1 := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())

	var (
		starts1      []*SessionEvent[*schema.Message]
		ends1        []*SessionEvent[*schema.Message]
		interruptEvt *AgentEvent
	)
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEvent != nil && ev.SessionEvent.Span != nil {
			switch ev.SessionEvent.Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEvent)
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEvent)
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 1)
	require.Empty(t, ends1)

	var toolInterruptID string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			toolInterruptID = ictx.ID
			break
		}
	}
	require.NotEmpty(t, toolInterruptID)

	resumeIter, err := runner.ResumeWithParams(ctx, checkpointID, &ResumeParams{
		Targets: map[string]any{toolInterruptID: &approvalResultSpan{Approved: true}},
	}, WithTimelineEvents())
	require.NoError(t, err)
	_, ends2, _ := drainAndCollectSpans(t, resumeIter)
	require.Len(t, ends2, 1)
	return starts1[0], ends2[0]
}

func TestToolSpan_PermissionResumeEmitsEndSpan(t *testing.T) {
	tl := &approvableSpanTool{name: "approve_me"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call_resume", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "agent_resume", []tool.BaseTool{tl}, scripted)
	startSpan, endSpan := runInterruptResumeAndCollectSpans(t, agent, store, "ckpt-resume")

	assert.Equal(t, startSpan.Span.SpanID, endSpan.Span.SpanID, "end span must reuse the original SpanID")
	assert.Equal(t, startSpan.EventID, endSpan.Span.Tool.ToolCallStartEventID, "end's ToolCallStartEventID must match start's EventID")
	assert.Equal(t, "ok", endSpan.Span.Status)
	assert.NotEmpty(t, endSpan.Span.Tool.ToolResultMessageEventID)
}

// TestToolSpan_ResumeUsesOriginalTurnParentIDs (plan §4.5.1 #3) verifies that
// the resumed end span's ParentSpanID and AssistantMessageEventID match the
// original turn's model span and assistant message — confirming the in-flight
// span snapshot survived the checkpoint round-trip.
func TestToolSpan_ResumeUsesOriginalTurnParentIDs(t *testing.T) {
	tl := &approvableSpanTool{name: "approve_me"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call_parents", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "agent_parents", []tool.BaseTool{tl}, scripted)
	startSpan, endSpan := runInterruptResumeAndCollectSpans(t, agent, store, "ckpt-parents")

	require.NotEmpty(t, startSpan.Span.ParentSpanID, "start span carries a non-empty parent (model) span ID")
	require.NotEmpty(t, startSpan.Span.Tool.AssistantMessageEventID, "start span carries a non-empty assistant message event ID")
	assert.Equal(t, startSpan.Span.ParentSpanID, endSpan.Span.ParentSpanID, "ParentSpanID survives resume via the in-flight snapshot")
	assert.Equal(t, startSpan.Span.Tool.AssistantMessageEventID, endSpan.Span.Tool.AssistantMessageEventID, "AssistantMessageEventID survives resume via the in-flight snapshot")
}

func TestToolSpan_HardErrorOnFirstRunStillEmitsEnd(t *testing.T) {
	ctx := context.Background()
	tl := &alwaysErrorTool{name: "boom"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "err_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
	}
	agent, store := setupApprovableSpanAgent(t, "err_agent", []tool.BaseTool{tl}, scripted)
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID("ckpt-err"), WithTimelineEvents())
	starts, ends, _ := drainAndCollectSpans(t, iter)

	require.Len(t, starts, 1)
	require.Len(t, ends, 1)
	assert.Equal(t, starts[0].Span.SpanID, ends[0].Span.SpanID, "end span shares SpanID with start span")
	assert.Equal(t, "error", ends[0].Span.Status)
}

func TestToolSpan_StreamableInterruptDefersEnd(t *testing.T) {
	ctx := context.Background()
	tl := &approvableStreamableSpanTool{name: "stream_approve_me"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "stream_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "stream_agent", []tool.BaseTool{tl}, scripted)
	runner1 := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	checkpointID := "ckpt-stream"
	iter1 := runner1.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())
	var interruptEvt *AgentEvent
	starts1, ends1 := []*SessionEvent[*schema.Message]{}, []*SessionEvent[*schema.Message]{}
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEvent != nil && ev.SessionEvent.Span != nil {
			switch ev.SessionEvent.Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEvent)
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEvent)
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 1, "one start span on interrupted streamable run")
	assert.Empty(t, ends1, "no end span on interrupted streamable run")
	startSpanID := starts1[0].Span.SpanID

	var toolInterruptID string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			toolInterruptID = ictx.ID
			break
		}
	}
	require.NotEmpty(t, toolInterruptID)

	resumeIter, err := runner1.ResumeWithParams(ctx, checkpointID, &ResumeParams{
		Targets: map[string]any{toolInterruptID: &approvalResultSpan{Approved: true}},
	}, WithTimelineEvents())
	require.NoError(t, err)
	starts2, ends2, _ := drainAndCollectSpans(t, resumeIter)
	assert.Empty(t, starts2, "no new start span on streamable resume")
	require.Len(t, ends2, 1, "one end span on streamable resume")
	assert.Equal(t, startSpanID, ends2[0].Span.SpanID, "end span shares SpanID with start span across resume")
	assert.Equal(t, "ok", ends2[0].Span.Status)
}

func TestTypedState_ToolSpansInFlightGobRoundTrip(t *testing.T) {
	original := &typedState[*schema.Message]{
		Messages:                       []*schema.Message{schema.UserMessage("hello")},
		CurrentModelSpanID:             "model-span-1",
		CurrentAssistantMessageEventID: "asst-event-1",
		ToolSpansInFlight: map[string]*toolSpanInFlight{
			"call_a": {
				SpanID:                  "span-a",
				StartEventID:            "start-event-a",
				StartedAt:               time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
				ParentSpanID:            "model-span-1",
				AssistantMessageEventID: "asst-event-1",
			},
			"call_b": {
				SpanID:                  "span-b",
				StartEventID:            "start-event-b",
				StartedAt:               time.Date(2026, 5, 26, 12, 0, 1, 0, time.UTC),
				ParentSpanID:            "model-span-1",
				AssistantMessageEventID: "asst-event-1",
			},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(original))

	decoded := &typedState[*schema.Message]{}
	require.NoError(t, gob.NewDecoder(&buf).Decode(decoded))

	assert.Equal(t, original.CurrentModelSpanID, decoded.CurrentModelSpanID)
	assert.Equal(t, original.CurrentAssistantMessageEventID, decoded.CurrentAssistantMessageEventID)
	require.Len(t, decoded.ToolSpansInFlight, 2)
	for k, v := range original.ToolSpansInFlight {
		got, ok := decoded.ToolSpansInFlight[k]
		require.Truef(t, ok, "missing key %q after gob round-trip", k)
		assert.Equal(t, v.SpanID, got.SpanID)
		assert.Equal(t, v.StartEventID, got.StartEventID)
		assert.True(t, v.StartedAt.Equal(got.StartedAt), "StartedAt mismatch: %v vs %v", v.StartedAt, got.StartedAt)
		assert.Equal(t, v.ParentSpanID, got.ParentSpanID)
		assert.Equal(t, v.AssistantMessageEventID, got.AssistantMessageEventID)
	}
}

// Sanity guard: ensure compose.IsInterruptRerunError import is preserved (used in wrappers).
var _ = compose.IsInterruptRerunError

// TestToolSpan_PermissionRejectEmitsEndSpan exercises the path where the tool
// is interrupted, then on resume the user rejects (Approved=false). The tool
// returns a rejection result rather than an error, so the end span carries
// Status=ok with a populated ToolResultMessageEventID. Same SpanID across
// the boundary.
func TestToolSpan_PermissionRejectEmitsEndSpan(t *testing.T) {
	ctx := context.Background()
	tl := &approvableSpanTool{name: "reject_me"}

	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "rej_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "reject_agent", []tool.BaseTool{tl}, scripted)
	checkpointID := "ckpt-reject"
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter1 := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())

	var (
		starts1      []*SessionEvent[*schema.Message]
		ends1        []*SessionEvent[*schema.Message]
		interruptEvt *AgentEvent
	)
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEvent != nil && ev.SessionEvent.Span != nil {
			switch ev.SessionEvent.Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEvent)
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEvent)
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 1)
	require.Empty(t, ends1)

	startSpanID := starts1[0].Span.SpanID

	var toolInterruptID string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			toolInterruptID = ictx.ID
			break
		}
	}
	require.NotEmpty(t, toolInterruptID)

	resumeIter, err := runner.ResumeWithParams(ctx, checkpointID, &ResumeParams{
		Targets: map[string]any{toolInterruptID: &approvalResultSpan{Approved: false}},
	}, WithTimelineEvents())
	require.NoError(t, err)
	starts2, ends2, _ := drainAndCollectSpans(t, resumeIter)
	assert.Empty(t, starts2)
	require.Len(t, ends2, 1)
	assert.Equal(t, startSpanID, ends2[0].Span.SpanID)
	assert.Equal(t, "ok", ends2[0].Span.Status, "rejection produces a successful return (the deny content) — status is ok, not error")
	assert.NotEmpty(t, ends2[0].Span.Tool.ToolResultMessageEventID)
}

// successOnlyTool runs to completion on the first invocation. Combined with
// the absence of a permission middleware, it exercises the "non-interrupted
// call" path where start and end both fire on the same run with status=ok.
type successOnlyTool struct {
	name   string
	result string
}

func (t *successOnlyTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "always succeeds",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input"},
		}),
	}, nil
}

func (t *successOnlyTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return t.result, nil
}

// TestToolSpan_NonInterruptedCallEmitsBothSpansOnSameRun verifies that a tool
// that runs straight to success produces a tool_call_start + tool_call_end
// pair on the same run, with the in-flight entry cleared at end emission.
// (This corresponds to plan §4.5.1 #6 which used a "gate=deny" example —
// the wire-shape behavior is identical: single run, single span pair, status
// ok, populated ToolResultMessageEventID.)
func TestToolSpan_NonInterruptedCallEmitsBothSpansOnSameRun(t *testing.T) {
	ctx := context.Background()
	tl := &successOnlyTool{name: "noninterrupted_tool", result: "ok"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling", []schema.ToolCall{{ID: "noninter_call", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"x"}`}}}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "noninter_agent", []tool.BaseTool{tl}, scripted)
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID("ckpt-noninter"), WithTimelineEvents())
	starts, ends, _ := drainAndCollectSpans(t, iter)

	require.Len(t, starts, 1)
	require.Len(t, ends, 1)
	assert.Equal(t, starts[0].Span.SpanID, ends[0].Span.SpanID)
	assert.Equal(t, "ok", ends[0].Span.Status)
	assert.NotEmpty(t, ends[0].Span.Tool.ToolResultMessageEventID)
}

// TestToolSpan_ParallelInterruptResumesEmitMatchingEnds exercises the parallel
// call scenario: two tool calls (A, B) emitted in a single assistant message,
// both interrupting on first invocation. After the first run we should see
// 2 starts and 0 ends. After resuming both with approval, we expect end spans
// keyed to the matching SpanIDs (one per CallID).
func TestToolSpan_ParallelInterruptResumesEmitMatchingEnds(t *testing.T) {
	ctx := context.Background()
	tl := &approvableSpanTool{name: "parallel_tool"}
	scripted := []*schema.Message{
		schema.AssistantMessage("calling 2", []schema.ToolCall{
			{ID: "call_par_a", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"a"}`}},
			{ID: "call_par_b", Function: schema.FunctionCall{Name: tl.name, Arguments: `{"input":"b"}`}},
		}),
		schema.AssistantMessage("done", nil),
	}
	agent, store := setupApprovableSpanAgent(t, "parallel_agent", []tool.BaseTool{tl}, scripted)
	checkpointID := "ckpt-parallel"
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	iter1 := runner.Run(ctx, []Message{schema.UserMessage("go")}, WithCheckPointID(checkpointID), WithTimelineEvents())

	var (
		starts1      []*SessionEvent[*schema.Message]
		ends1        []*SessionEvent[*schema.Message]
		interruptEvt *AgentEvent
	)
	for {
		ev, ok := iter1.Next()
		if !ok {
			break
		}
		if ev.Action != nil && ev.Action.Interrupted != nil {
			interruptEvt = ev
		}
		if ev.SessionEvent != nil && ev.SessionEvent.Span != nil {
			switch ev.SessionEvent.Kind {
			case SessionEventSpanToolCallStart:
				starts1 = append(starts1, ev.SessionEvent)
			case SessionEventSpanToolCallEnd:
				ends1 = append(ends1, ev.SessionEvent)
			}
		}
	}
	require.NotNil(t, interruptEvt)
	require.Len(t, starts1, 2, "expected one tool_call_start for each parallel call")
	assert.Empty(t, ends1)

	// Map CallID -> start SpanID for later assertions.
	callIDToStartSpanID := map[string]string{}
	for _, s := range starts1 {
		callIDToStartSpanID[s.Span.Tool.ToolUseID] = s.Span.SpanID
	}
	require.Contains(t, callIDToStartSpanID, "call_par_a")
	require.Contains(t, callIDToStartSpanID, "call_par_b")

	// Collect interrupt IDs (root causes only).
	var interruptIDs []string
	for _, ictx := range interruptEvt.Action.Interrupted.InterruptContexts {
		if ictx.IsRootCause {
			interruptIDs = append(interruptIDs, ictx.ID)
		}
	}
	require.Len(t, interruptIDs, 2)

	// Approve both at once.
	targets := map[string]any{}
	for _, id := range interruptIDs {
		targets[id] = &approvalResultSpan{Approved: true}
	}
	resumeIter, err := runner.ResumeWithParams(ctx, checkpointID, &ResumeParams{Targets: targets}, WithTimelineEvents())
	require.NoError(t, err)
	starts2, ends2, _ := drainAndCollectSpans(t, resumeIter)
	assert.Empty(t, starts2, "no new starts on resume")
	require.Len(t, ends2, 2, "two ends, one per parallel call")
	for _, e := range ends2 {
		expectedSpanID, ok := callIDToStartSpanID[e.Span.Tool.ToolUseID]
		require.Truef(t, ok, "end span carries unknown CallID %q", e.Span.Tool.ToolUseID)
		assert.Equal(t, expectedSpanID, e.Span.SpanID, "end span SpanID matches the start span for the same CallID")
		assert.Equal(t, "ok", e.Span.Status)
	}
}
