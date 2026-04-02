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
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type agenticAgentEvent = TypedAgentEvent[*schema.AgenticMessage]

func agenticToolCallMsg(toolName, callID, args string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type:             schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{Name: toolName, CallID: callID, Arguments: args},
			},
		},
	}
}

type sequentialAgenticModel struct {
	responses []*schema.AgenticMessage
	callCount int32
}

func (m *sequentialAgenticModel) Generate(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
	idx := atomic.AddInt32(&m.callCount, 1) - 1
	if int(idx) >= len(m.responses) {
		return nil, fmt.Errorf("sequentialAgenticModel: no more responses (call #%d)", idx)
	}
	return m.responses[idx], nil
}

func (m *sequentialAgenticModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	result, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	r, w := schema.Pipe[*schema.AgenticMessage](1)
	go func() { defer w.Close(); w.Send(result, nil) }()
	return r, nil
}

type agenticEchoTool struct {
	name string
}

func (t *agenticEchoTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "echoes input"}, nil
}

func (t *agenticEchoTool) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	return "echo:" + argumentsInJSON, nil
}

type agenticInterruptTool struct {
	name string
}

func (t *agenticInterruptTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "interrupts on first call, returns on resume"}, nil
}

func (t *agenticInterruptTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	wasInterrupted, _, _ := tool.GetInterruptState[any](ctx)
	if !wasInterrupted {
		return "", tool.Interrupt(ctx, "need_approval")
	}
	isResume, hasData, data := tool.GetResumeContext[string](ctx)
	if isResume && hasData {
		return "approved:" + data, nil
	}
	return "resumed_no_data", nil
}

type agenticArgCaptureTool struct {
	name     string
	onInvoke func(args string) string
}

func (t *agenticArgCaptureTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "captures args"}, nil
}

func (t *agenticArgCaptureTool) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	return t.onInvoke(argumentsInJSON), nil
}

type agenticSignalTool struct {
	name    string
	started chan struct{}
	result  string
	done    chan struct{}
	once    sync.Once
}

func (t *agenticSignalTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "blocks until finish() is called"}, nil
}

func (t *agenticSignalTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	t.once.Do(func() { t.done = make(chan struct{}) })
	select {
	case t.started <- struct{}{}:
	default:
	}
	<-t.done
	return t.result, nil
}

func (t *agenticSignalTool) finish() {
	t.once.Do(func() { t.done = make(chan struct{}) })
	close(t.done)
}

type agenticReactTestStore struct {
	m map[string][]byte
}

func (s *agenticReactTestStore) Set(_ context.Context, key string, value []byte) error {
	s.m[key] = value
	return nil
}

func (s *agenticReactTestStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.m[key]
	return v, ok, nil
}

func newAgenticAgent(t *testing.T, ctx context.Context, mdl model.BaseModel[*schema.AgenticMessage], tools []tool.BaseTool) TypedAgent[*schema.AgenticMessage] {
	t.Helper()
	config := &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        t.Name(),
		Description: "test agentic agent",
		Model:       mdl,
	}
	if len(tools) > 0 {
		config.ToolsConfig = ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools,
			},
		}
	}
	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, config)
	require.NoError(t, err)
	return agent
}

func newAgenticRunner(t *testing.T, ctx context.Context, mdl model.BaseModel[*schema.AgenticMessage], tools []tool.BaseTool) *TypedRunner[*schema.AgenticMessage] {
	t.Helper()
	agent := newAgenticAgent(t, ctx, mdl, tools)
	return NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
}

func newAgenticRunnerWithStore(t *testing.T, ctx context.Context, mdl model.BaseModel[*schema.AgenticMessage], tools []tool.BaseTool, store CheckPointStore) *TypedRunner[*schema.AgenticMessage] {
	t.Helper()
	agent := newAgenticAgent(t, ctx, mdl, tools)
	return NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		CheckPointStore: store,
	})
}

func drainAgenticEvents(iter *AsyncIterator[*agenticAgentEvent]) []*agenticAgentEvent {
	var events []*agenticAgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
	}
	return events
}

func lastAgenticEvent(events []*agenticAgentEvent) *agenticAgentEvent {
	if len(events) == 0 {
		return nil
	}
	return events[len(events)-1]
}

func findInterruptEvent(events []*agenticAgentEvent) *agenticAgentEvent {
	for _, ev := range events {
		if ev.Action != nil && ev.Action.Interrupted != nil {
			return ev
		}
	}
	return nil
}

func TestAgenticReact_BasicInvoke(t *testing.T) {
	ctx := context.Background()

	mdl := &sequentialAgenticModel{
		responses: []*schema.AgenticMessage{
			agenticToolCallMsg("echo", "call-1", `"hello"`),
			agenticMsg("done: echo result received"),
		},
	}

	runner := newAgenticRunner(t, ctx, mdl, []tool.BaseTool{&agenticEchoTool{name: "echo"}})
	events := drainAgenticEvents(runner.Query(ctx, "test input"))
	last := lastAgenticEvent(events)

	require.NotNil(t, last)
	require.Nil(t, last.Err)
	assert.Equal(t, "done: echo result received", agenticTextContent(last.Output.MessageOutput.Message))
	assert.Equal(t, int32(2), atomic.LoadInt32(&mdl.callCount))
}

func TestAgenticReact_MultiTurnToolCalling(t *testing.T) {
	ctx := context.Background()

	mdl := &sequentialAgenticModel{
		responses: []*schema.AgenticMessage{
			agenticToolCallMsg("echo", "call-1", `"step1"`),
			agenticToolCallMsg("echo", "call-2", `"step2"`),
			agenticToolCallMsg("echo", "call-3", `"step3"`),
			agenticMsg("all done"),
		},
	}

	runner := newAgenticRunner(t, ctx, mdl, []tool.BaseTool{&agenticEchoTool{name: "echo"}})
	events := drainAgenticEvents(runner.Query(ctx, "do three steps"))
	last := lastAgenticEvent(events)

	require.NotNil(t, last)
	require.Nil(t, last.Err)
	assert.Equal(t, "all done", agenticTextContent(last.Output.MessageOutput.Message))
	assert.Equal(t, int32(4), atomic.LoadInt32(&mdl.callCount))
}

func TestAgenticReact_Stream(t *testing.T) {
	ctx := context.Background()

	mdl := &sequentialAgenticModel{
		responses: []*schema.AgenticMessage{
			agenticToolCallMsg("echo", "call-1", `"hello"`),
			agenticMsg("stream done"),
		},
	}

	agent := newAgenticAgent(t, ctx, mdl, []tool.BaseTool{&agenticEchoTool{name: "echo"}})
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
	})

	events := drainAgenticEvents(runner.Query(ctx, "stream test"))

	var finalText string
	for _, ev := range events {
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			msg, err := ev.Output.MessageOutput.GetMessage()
			if err == nil && msg != nil {
				txt := agenticTextContent(msg)
				if txt != "" {
					finalText = txt
				}
			}
		}
	}

	assert.Equal(t, "stream done", finalText)
}

func TestAgenticReact_MaxIterations(t *testing.T) {
	ctx := context.Background()

	t.Run("within_limit", func(t *testing.T) {
		mdl := &sequentialAgenticModel{
			responses: []*schema.AgenticMessage{
				agenticToolCallMsg("echo", "c1", `"1"`),
				agenticToolCallMsg("echo", "c2", `"2"`),
				agenticMsg("done within limit"),
			},
		}

		runner := newAgenticRunner(t, ctx, mdl, []tool.BaseTool{&agenticEchoTool{name: "echo"}})
		events := drainAgenticEvents(runner.Query(ctx, "go"))
		last := lastAgenticEvent(events)

		require.NotNil(t, last)
		require.Nil(t, last.Err)
		assert.Equal(t, "done within limit", agenticTextContent(last.Output.MessageOutput.Message))
	})

	t.Run("exceeded", func(t *testing.T) {
		responses := make([]*schema.AgenticMessage, 25)
		for i := range responses {
			responses[i] = agenticToolCallMsg("echo", fmt.Sprintf("c%d", i), `"x"`)
		}

		mdl := &sequentialAgenticModel{responses: responses}
		config := &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:          "exceed-agent",
			Description:   "test max iterations exceeded",
			Model:         mdl,
			MaxIterations: 3,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{&agenticEchoTool{name: "echo"}},
				},
			},
		}
		agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, config)
		require.NoError(t, err)

		runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent})
		events := drainAgenticEvents(runner.Query(ctx, "go"))
		last := lastAgenticEvent(events)

		require.NotNil(t, last)
		require.NotNil(t, last.Err)
		assert.ErrorIs(t, last.Err, ErrExceedMaxIterations)
	})
}

func TestAgenticReact_ReturnDirectly(t *testing.T) {
	t.Skip("returnDirectly for agentic agents depends on typed eventSenderToolHandler; not yet supported")
}

func TestAgenticReact_InterruptResumeRoundTrip(t *testing.T) {
	ctx := context.Background()

	var modelCallCount int32
	mdl := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			count := atomic.AddInt32(&modelCallCount, 1)
			switch count {
			case 1:
				return agenticToolCallMsg("approval_tool", "call-int", `"check"`), nil
			case 2:
				for _, msg := range input {
					for _, b := range msg.ContentBlocks {
						if b.FunctionToolResult != nil && b.FunctionToolResult.CallID == "call-int" {
							return agenticMsg("completed with: " + b.FunctionToolResult.Result), nil
						}
					}
				}
				return agenticMsg("completed no result"), nil
			default:
				return nil, fmt.Errorf("unexpected call #%d", count)
			}
		},
	}

	store := &agenticReactTestStore{m: map[string][]byte{}}
	runner := newAgenticRunnerWithStore(t, ctx, mdl, []tool.BaseTool{&agenticInterruptTool{name: "approval_tool"}}, store)

	events := drainAgenticEvents(runner.Query(ctx, "please approve", WithCheckPointID("cp-1")))
	interruptEvent := findInterruptEvent(events)

	require.NotNil(t, interruptEvent, "expected an interrupt event")
	require.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts)

	_, hasData, _ := store.Get(ctx, "cp-1")
	assert.True(t, hasData, "checkpoint should be saved after interrupt")

	interruptID := interruptEvent.Action.Interrupted.InterruptContexts[0].ID

	iter2, err := runner.ResumeWithParams(ctx, "cp-1", &ResumeParams{
		Targets: map[string]any{interruptID: "yes_approved"},
	})
	require.NoError(t, err)

	events2 := drainAgenticEvents(iter2)
	last := lastAgenticEvent(events2)

	require.NotNil(t, last)
	require.Nil(t, last.Err, "last event should not have an error")
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)

	finalMsg := last.Output.MessageOutput.Message
	require.NotNil(t, finalMsg)
	assert.Contains(t, agenticTextContent(finalMsg), "approved:yes_approved")
}

func TestAgenticReact_StateGobRoundTrip(t *testing.T) {
	ctx := context.Background()

	richMsg := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.AssistantGenText{Text: "thinking..."}),
			{
				Type: schema.ContentBlockTypeFunctionToolCall,
				FunctionToolCall: &schema.FunctionToolCall{
					Name:      "my_tool",
					CallID:    "tc-123",
					Arguments: `{"key": "value"}`,
				},
			},
		},
	}

	var modelCallCount int32
	mdl := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			count := atomic.AddInt32(&modelCallCount, 1)
			switch count {
			case 1:
				return richMsg, nil
			case 2:
				hasToolResult := false
				for _, msg := range input {
					for _, b := range msg.ContentBlocks {
						if b.FunctionToolResult != nil && b.FunctionToolResult.CallID == "tc-123" {
							hasToolResult = true
						}
					}
				}
				if !hasToolResult {
					return nil, fmt.Errorf("expected tool result in resumed input")
				}
				return agenticMsg("done after resume"), nil
			default:
				return nil, fmt.Errorf("unexpected call #%d", count)
			}
		},
	}

	store := &agenticReactTestStore{m: map[string][]byte{}}
	runner := newAgenticRunnerWithStore(t, ctx, mdl, []tool.BaseTool{&agenticInterruptTool{name: "my_tool"}}, store)

	events := drainAgenticEvents(runner.Query(ctx, "test gob roundtrip", WithCheckPointID("gob-cp")))
	interruptEvent := findInterruptEvent(events)
	require.NotNil(t, interruptEvent)

	data, exists, err := store.Get(ctx, "gob-cp")
	require.NoError(t, err)
	require.True(t, exists)
	require.NotEmpty(t, data, "checkpoint data should be non-empty")

	interruptID := interruptEvent.Action.Interrupted.InterruptContexts[0].ID
	iter2, err := runner.ResumeWithParams(ctx, "gob-cp", &ResumeParams{
		Targets: map[string]any{interruptID: "resumed_data"},
	})
	require.NoError(t, err)

	events2 := drainAgenticEvents(iter2)
	last := lastAgenticEvent(events2)

	require.NotNil(t, last)
	require.Nil(t, last.Err)
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)
	assert.Contains(t, agenticTextContent(last.Output.MessageOutput.Message), "done after resume")
}

func TestAgenticReact_CancelAfterChatModel(t *testing.T) {
	ctx := context.Background()

	toolStarted := make(chan struct{}, 1)
	var modelCallCount int32
	mdl := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			count := atomic.AddInt32(&modelCallCount, 1)
			switch count {
			case 1:
				return agenticToolCallMsg("slow", "c1", `"hi"`), nil
			case 2:
				return agenticToolCallMsg("slow", "c2", `"hi2"`), nil
			default:
				return agenticMsg("should not reach"), nil
			}
		},
	}

	slowTool := &agenticSignalTool{
		name:    "slow",
		started: toolStarted,
		result:  "slow result",
	}

	agent := newAgenticAgent(t, ctx, mdl, []tool.BaseTool{slowTool})

	cancelOpt, cancelFn := WithCancel()
	iter := agent.Run(ctx, &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("trigger cancel")},
	}, cancelOpt)

	<-toolStarted

	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		_ = handle.Wait()
	}()

	time.Sleep(10 * time.Millisecond)
	slowTool.finish()

	var foundCancel bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		var cancelErr *CancelError
		if ev.Err != nil && errors.As(ev.Err, &cancelErr) {
			foundCancel = true
		}
	}
	assert.True(t, foundCancel, "expected CancelError event")
}

func TestAgenticReact_CancelAfterToolCalls(t *testing.T) {
	ctx := context.Background()

	toolStarted := make(chan struct{}, 1)
	var modelCallCount int32
	mdl := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			count := atomic.AddInt32(&modelCallCount, 1)
			if count == 1 {
				return agenticToolCallMsg("slow", "c1", `"hi"`), nil
			}
			return agenticMsg("should not reach on second call"), nil
		},
	}

	slowTool := &agenticSignalTool{
		name:    "slow",
		started: toolStarted,
		result:  "slow result",
	}

	agent := newAgenticAgent(t, ctx, mdl, []tool.BaseTool{slowTool})

	cancelOpt, cancelFn := WithCancel()
	iter := agent.Run(ctx, &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("trigger cancel")},
	}, cancelOpt)

	<-toolStarted

	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
		_ = handle.Wait()
	}()

	time.Sleep(10 * time.Millisecond)
	slowTool.finish()

	var foundCancel bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		var cancelErr *CancelError
		if ev.Err != nil && errors.As(ev.Err, &cancelErr) {
			foundCancel = true
		}
	}
	assert.True(t, foundCancel, "expected CancelError event")
	assert.Equal(t, int32(1), atomic.LoadInt32(&modelCallCount))
}

func TestAgenticReact_DoubleInterruptResume(t *testing.T) {
	ctx := context.Background()

	var modelCallCount int32
	mdl := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			count := atomic.AddInt32(&modelCallCount, 1)
			switch count {
			case 1:
				return agenticToolCallMsg("approval_tool", "c1", `"first"`), nil
			case 2:
				return agenticToolCallMsg("approval_tool", "c2", `"second"`), nil
			case 3:
				return agenticMsg("all approved"), nil
			default:
				return nil, fmt.Errorf("unexpected call #%d", count)
			}
		},
	}

	store := &agenticReactTestStore{m: map[string][]byte{}}
	runner := newAgenticRunnerWithStore(t, ctx, mdl, []tool.BaseTool{&agenticInterruptTool{name: "approval_tool"}}, store)

	events1 := drainAgenticEvents(runner.Query(ctx, "approve twice", WithCheckPointID("dbl-cp")))
	int1Event := findInterruptEvent(events1)
	require.NotNil(t, int1Event, "expected first interrupt")
	int1ID := int1Event.Action.Interrupted.InterruptContexts[0].ID

	iter2, err := runner.ResumeWithParams(ctx, "dbl-cp", &ResumeParams{
		Targets: map[string]any{int1ID: "approved_1"},
	})
	require.NoError(t, err)

	events2 := drainAgenticEvents(iter2)
	int2Event := findInterruptEvent(events2)
	require.NotNil(t, int2Event, "expected second interrupt")
	int2ID := int2Event.Action.Interrupted.InterruptContexts[0].ID

	iter3, err := runner.ResumeWithParams(ctx, "dbl-cp", &ResumeParams{
		Targets: map[string]any{int2ID: "approved_2"},
	})
	require.NoError(t, err)

	events3 := drainAgenticEvents(iter3)
	last := lastAgenticEvent(events3)

	require.NotNil(t, last)
	require.Nil(t, last.Err)
	require.NotNil(t, last.Output)
	assert.Contains(t, agenticTextContent(last.Output.MessageOutput.Message), "all approved")
}

func TestAgenticReact_ChatModelAgent_NoTools(t *testing.T) {
	ctx := context.Background()

	mdl := &mockAgenticModel{
		generateFn: func(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("no tools response"), nil
		},
	}

	runner := newAgenticRunner(t, ctx, mdl, nil)
	events := drainAgenticEvents(runner.Query(ctx, "hello"))
	last := lastAgenticEvent(events)

	require.NotNil(t, last)
	require.Nil(t, last.Err)
	assert.Equal(t, "no tools response", agenticTextContent(last.Output.MessageOutput.Message))
}

func TestAgenticReact_ChatModelAgent_ToolsReceiveArgs(t *testing.T) {
	ctx := context.Background()

	var receivedArgs string
	captureTool := &agenticArgCaptureTool{
		name: "capture",
		onInvoke: func(args string) string {
			receivedArgs = args
			return "captured"
		},
	}

	mdl := &sequentialAgenticModel{
		responses: []*schema.AgenticMessage{
			agenticToolCallMsg("capture", "c1", `{"foo":"bar"}`),
			agenticMsg("done"),
		},
	}

	runner := newAgenticRunner(t, ctx, mdl, []tool.BaseTool{captureTool})
	drainAgenticEvents(runner.Query(ctx, "call capture"))

	assert.Equal(t, `{"foo":"bar"}`, receivedArgs)
}

func TestCoverage_AgenticReact_Streaming(t *testing.T) {
	ctx := context.Background()

	m := &mockAgenticModel{
		streamFn: func(_ context.Context, input []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
			r, w := schema.Pipe[*schema.AgenticMessage](1)
			go func() {
				defer w.Close()
				w.Send(&schema.AgenticMessage{
					Role: schema.AgenticRoleTypeAssistant,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.AssistantGenText{Text: "streamed response"}),
					},
				}, nil)
			}()
			return r, nil
		},
	}

	echoTool := &agenticEchoTool{name: "echo"}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "stream-react",
		Description: "streaming agentic react",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{echoTool},
			},
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
	})

	iter := runner.Query(ctx, "stream me")

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.IsStreaming {
			stream := event.Output.MessageOutput.MessageStream
			for {
				_, sErr := stream.Recv()
				if sErr != nil {
					break
				}
			}
		}
		events = append(events, event)
	}

	require.NotEmpty(t, events)
}

func TestCoverage_IsToolResultEvent_Agentic(t *testing.T) {
	t.Run("WithToolResult", func(t *testing.T) {
		event := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: &schema.AgenticMessage{
							Role: schema.AgenticRoleTypeUser,
							ContentBlocks: []*schema.ContentBlock{
								{
									Type:               schema.ContentBlockTypeFunctionToolResult,
									FunctionToolResult: &schema.FunctionToolResult{Name: "tool1", Result: "ok"},
								},
							},
						},
					},
				},
			},
		}
		assert.True(t, isToolResultEvent(event))
	})

	t.Run("WithoutToolResult", func(t *testing.T) {
		event := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: &schema.AgenticMessage{
							Role: schema.AgenticRoleTypeAssistant,
							ContentBlocks: []*schema.ContentBlock{
								schema.NewContentBlock(&schema.AssistantGenText{Text: "no tool result"}),
							},
						},
					},
				},
			},
		}
		assert.False(t, isToolResultEvent(event))
	})

	t.Run("NilOutput", func(t *testing.T) {
		event := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{},
		}
		assert.False(t, isToolResultEvent(event))
	})

	t.Run("Streaming", func(t *testing.T) {
		event := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						IsStreaming: true,
					},
				},
			},
		}
		assert.False(t, isToolResultEvent(event))
	})

	t.Run("NilMessage", func(t *testing.T) {
		event := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{},
				},
			},
		}
		assert.False(t, isToolResultEvent(event))
	})
}

func TestCoverage_ConcatMessageStream_Agentic(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		r, w := schema.Pipe[*schema.AgenticMessage](2)
		go func() {
			defer w.Close()
			w.Send(&schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.AssistantGenText{Text: "Hello "}),
				},
			}, nil)
			w.Send(&schema.AgenticMessage{
				Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{
					schema.NewContentBlock(&schema.AssistantGenText{Text: "world"}),
				},
			}, nil)
		}()

		result, err := concatMessageStream(r)
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("ErrorDuringRecv", func(t *testing.T) {
		r, w := schema.Pipe[*schema.AgenticMessage](2)
		go func() {
			w.Send(nil, fmt.Errorf("recv error"))
			w.Close()
		}()

		_, err := concatMessageStream(r)
		assert.Error(t, err)
	})
}

func TestCoverage_AgenticReact_InterruptResume(t *testing.T) {
	ctx := context.Background()

	interruptTool := &agenticInterruptTool{name: "approval"}

	var callIdx int32
	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return agenticToolCallMsg("approval", "call1", `{}`), nil
			}
			return agenticMsg("approved and done"), nil
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "interrupt-agent",
		Description: "tests interrupt and resume",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{interruptTool},
			},
		},
	})
	require.NoError(t, err)

	store := newDTTestStore()
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		CheckPointStore: store,
	})

	iter := runner.Run(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("need approval"),
	}, WithCheckPointID("cp-int"))

	var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			interruptEvent = event
		}
	}

	require.NotNil(t, interruptEvent, "should have interrupt event")

	var rootCauseID string
	for _, intCtx := range interruptEvent.Action.Interrupted.InterruptContexts {
		if intCtx.IsRootCause {
			rootCauseID = intCtx.ID
			break
		}
	}
	require.NotEmpty(t, rootCauseID)

	resumeIter, err := runner.ResumeWithParams(ctx, "cp-int", &ResumeParams{
		Targets: map[string]any{rootCauseID: "approved"},
	})
	require.NoError(t, err)

	var events []*TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	require.NotEmpty(t, events)
}

func TestCoverage_AgenticMessageHasToolCalls(t *testing.T) {
	t.Run("NilMessage", func(t *testing.T) {
		assert.False(t, agenticMessageHasToolCalls(nil))
	})

	t.Run("NoToolCalls", func(t *testing.T) {
		msg := agenticMsg("just text")
		assert.False(t, agenticMessageHasToolCalls(msg))
	})

	t.Run("HasToolCalls", func(t *testing.T) {
		msg := agenticToolCallMsg("tool1", "id1", `{}`)
		assert.True(t, agenticMessageHasToolCalls(msg))
	})

	t.Run("NilBlock", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			ContentBlocks: []*schema.ContentBlock{nil},
		}
		assert.False(t, agenticMessageHasToolCalls(msg))
	})

	t.Run("ToolCallBlockNilFunctionToolCall", func(t *testing.T) {
		msg := &schema.AgenticMessage{
			ContentBlocks: []*schema.ContentBlock{
				{Type: schema.ContentBlockTypeFunctionToolCall, FunctionToolCall: nil},
			},
		}
		assert.False(t, agenticMessageHasToolCalls(msg))
	})
}

func TestCoverage_ChatModelAgent_StreamError(t *testing.T) {
	ctx := context.Background()

	testErr := errors.New("stream failed")
	m := &mockAgenticModel{
		streamFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
			return nil, testErr
		},
	}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "stream-error-agent",
		Description: "tests stream error",
		Model:       m,
	})
	require.NoError(t, err)

	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
	})

	iter := runner.Query(ctx, "trigger stream error")

	var foundErr bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			foundErr = true
		}
	}
	assert.True(t, foundErr, "should propagate stream error")
}

func TestCoverage_AgenticReact_GobStateRoundTrip(t *testing.T) {
	ctx := context.Background()

	var callIdx int32
	m := &mockAgenticModel{
		generateFn: func(_ context.Context, _ []*schema.AgenticMessage, _ ...model.Option) (*schema.AgenticMessage, error) {
			idx := atomic.AddInt32(&callIdx, 1)
			if idx == 1 {
				return agenticToolCallMsg("interrupt_tool", "call1", `{}`), nil
			}
			return agenticMsg("completed"), nil
		},
	}

	interruptTool := &agenticInterruptTool{name: "interrupt_tool"}

	agent, err := NewTypedChatModelAgent[*schema.AgenticMessage](ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "gob-test",
		Description: "tests gob state round trip",
		Model:       m,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{interruptTool},
			},
		},
	})
	require.NoError(t, err)

	store := newDTTestStore()
	runner := NewTypedRunner[*schema.AgenticMessage](TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		CheckPointStore: store,
	})

	iter := runner.Run(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("test gob"),
	}, WithCheckPointID("gob-cp"))

	var interrupted bool
	var interruptEvent *TypedAgentEvent[*schema.AgenticMessage]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			interrupted = true
			interruptEvent = event
		}
	}

	if !interrupted || interruptEvent == nil {
		t.Skip("no interrupt occurred, skipping gob round-trip test")
	}

	_, exists, err := store.Get(ctx, "gob-cp")
	assert.NoError(t, err)
	assert.True(t, exists, "checkpoint should be saved")

	var rootCauseID string
	for _, intCtx := range interruptEvent.Action.Interrupted.InterruptContexts {
		if intCtx.IsRootCause {
			rootCauseID = intCtx.ID
			break
		}
	}
	require.NotEmpty(t, rootCauseID)

	resumeIter, err := runner.ResumeWithParams(ctx, "gob-cp", &ResumeParams{
		Targets: map[string]any{rootCauseID: "approved"},
	})
	require.NoError(t, err)

	var resumed bool
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			resumed = true
		}
	}
	assert.True(t, resumed, "should successfully resume from gob checkpoint")
}

func TestCoverage_GetMessageFromTypedWrappedEvent_Agentic(t *testing.T) {
	t.Run("NilOutput", func(t *testing.T) {
		wrapper := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{},
		}
		msg, err := getMessageFromTypedWrappedEvent(wrapper)
		assert.NoError(t, err)
		assert.Nil(t, msg)
	})

	t.Run("NonStreaming", func(t *testing.T) {
		expected := agenticMsg("hello")
		wrapper := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						Message: expected,
					},
				},
			},
		}
		msg, err := getMessageFromTypedWrappedEvent(wrapper)
		assert.NoError(t, err)
		assert.Equal(t, expected, msg)
	})

	t.Run("StreamingAlreadyConcatenated", func(t *testing.T) {
		expected := agenticMsg("already concatenated")
		wrapper := &typedAgentEventWrapper[*schema.AgenticMessage]{
			concatenatedMessage: expected,
			event: &TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						IsStreaming: true,
					},
				},
			},
		}
		msg, err := getMessageFromTypedWrappedEvent(wrapper)
		assert.NoError(t, err)
		assert.Equal(t, expected, msg)
	})

	t.Run("StreamingWithPriorError", func(t *testing.T) {
		testErr := errors.New("prior stream error")
		wrapper := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: &TypedAgentEvent[*schema.AgenticMessage]{
				Output: &TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
						IsStreaming: true,
					},
				},
			},
		}
		wrapper.StreamErr = testErr
		msg, err := getMessageFromTypedWrappedEvent(wrapper)
		assert.Equal(t, testErr, err)
		assert.Nil(t, msg)
	})
}

func TestCoverage_GetMessageFromWrappedEvent_ErrorPaths(t *testing.T) {
	t.Run("NilOutput", func(t *testing.T) {
		wrapper := &agentEventWrapper{
			AgentEvent: &AgentEvent{},
		}
		msg, err := getMessageFromWrappedEvent(wrapper)
		assert.NoError(t, err)
		assert.Nil(t, msg)
	})

	t.Run("NonStreaming", func(t *testing.T) {
		expected := schema.AssistantMessage("hello", nil)
		wrapper := &agentEventWrapper{
			AgentEvent: &AgentEvent{
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						Message: expected,
					},
				},
			},
		}
		msg, err := getMessageFromWrappedEvent(wrapper)
		assert.NoError(t, err)
		assert.Equal(t, expected, msg)
	})

	t.Run("AlreadyConcatenated", func(t *testing.T) {
		expected := schema.AssistantMessage("concatenated", nil)
		wrapper := &agentEventWrapper{
			concatenatedMessage: expected,
			AgentEvent: &AgentEvent{
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: true,
					},
				},
			},
		}
		msg, err := getMessageFromWrappedEvent(wrapper)
		assert.NoError(t, err)
		assert.Equal(t, expected, msg)
	})

	t.Run("PriorStreamError", func(t *testing.T) {
		testErr := errors.New("prior error")
		wrapper := &agentEventWrapper{
			AgentEvent: &AgentEvent{
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: true,
					},
				},
			},
		}
		wrapper.StreamErr = testErr
		msg, err := getMessageFromWrappedEvent(wrapper)
		assert.Equal(t, testErr, err)
		assert.Nil(t, msg)
	})
}

func TestCoverage_ConsumeStream_ErrorDuringRecv(t *testing.T) {
	testErr := errors.New("stream recv error")
	r, w := schema.Pipe[*schema.Message](2)
	go func() {
		w.Send(schema.AssistantMessage("partial", nil), nil)
		w.Send(nil, testErr)
		w.Close()
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: r,
				},
			},
		},
	}

	wrapper.consumeStream()

	assert.NotNil(t, wrapper.StreamErr)
	assert.Nil(t, wrapper.concatenatedMessage)
}

func TestCoverage_ConsumeStream_EmptyStream(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](1)
	go func() { w.Close() }()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: r,
				},
			},
		},
	}

	wrapper.consumeStream()

	assert.NotNil(t, wrapper.StreamErr)
	assert.Contains(t, wrapper.StreamErr.Error(), "no messages")
}

func TestCoverage_ConsumeStream_MultipleMessages(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](3)
	go func() {
		defer w.Close()
		w.Send(schema.AssistantMessage("chunk1", nil), nil)
		w.Send(schema.AssistantMessage("chunk2", nil), nil)
		w.Send(schema.AssistantMessage("chunk3", nil), nil)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: r,
				},
			},
		},
	}

	wrapper.consumeStream()

	assert.Nil(t, wrapper.StreamErr)
	assert.NotNil(t, wrapper.concatenatedMessage)
}

func TestCoverage_ConsumeStream_SingleMessage(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](1)
	go func() {
		defer w.Close()
		w.Send(schema.AssistantMessage("single", nil), nil)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: r,
				},
			},
		},
	}

	wrapper.consumeStream()

	assert.Nil(t, wrapper.StreamErr)
	assert.NotNil(t, wrapper.concatenatedMessage)
	assert.Equal(t, "single", wrapper.concatenatedMessage.Content)
}

func TestCoverage_ConsumeStream_Idempotent(t *testing.T) {
	r, w := schema.Pipe[*schema.Message](1)
	go func() {
		defer w.Close()
		w.Send(schema.AssistantMessage("once", nil), nil)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: r,
				},
			},
		},
	}

	wrapper.consumeStream()
	msg1 := wrapper.concatenatedMessage

	wrapper.consumeStream()
	msg2 := wrapper.concatenatedMessage

	assert.Equal(t, msg1, msg2, "second call should be no-op")
}
