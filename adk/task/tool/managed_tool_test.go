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

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	taskcore "github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
	"github.com/cloudwego/eino/components/model"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type fakeTool struct {
	start           func(context.Context, *StartRequest) (Run, error)
	startCheckpoint []byte
	recover         func(context.Context, *RecoverRequest) (Run, error)
}

type resumableFakeTool struct {
	*fakeTool
	resume func(context.Context, *ResumeRequest) (Run, error)
}

type autoBackgroundFakeTool struct {
	*fakeTool
}

type autoBackgroundResumableFakeTool struct {
	*resumableFakeTool
}

func (t *resumableFakeTool) Resume(
	ctx context.Context,
	request *ResumeRequest,
) (Run, error) {
	return t.resume(ctx, request)
}

type plainFakeTool struct {
	start           func(context.Context, *StartRequest) (Run, error)
	startCheckpoint []byte
}

type preparingPlainTool struct {
	*plainFakeTool
	prepare func(context.Context, string) (string, error)
}

func (t *preparingPlainTool) PrepareInput(
	ctx context.Context,
	arguments string,
) (string, error) {
	return t.prepare(ctx, arguments)
}

func (*plainFakeTool) ValidateArguments(arguments string) error {
	var value map[string]any
	return json.Unmarshal([]byte(arguments), &value)
}
func (t *plainFakeTool) Start(
	ctx context.Context,
	request *StartRequest,
) (*StartResult, error) {
	run, err := t.start(ctx, request)
	if err != nil {
		return nil, err
	}
	return &StartResult{
		Run: run, Checkpoint: append([]byte(nil), t.startCheckpoint...),
	}, nil
}

func (*fakeTool) ValidateArguments(arguments string) error {
	var value map[string]any
	return json.Unmarshal([]byte(arguments), &value)
}
func (t *fakeTool) Start(
	ctx context.Context,
	request *StartRequest,
) (*StartResult, error) {
	run, err := t.start(ctx, request)
	if err != nil {
		return nil, err
	}
	return &StartResult{
		Run: run, Checkpoint: append([]byte(nil), t.startCheckpoint...),
	}, nil
}
func (t *fakeTool) Recover(ctx context.Context, request *RecoverRequest) (Run, error) {
	return t.recover(ctx, request)
}

type fakeRun struct {
	wait func(context.Context) (*Outcome, error)
	stop func(context.Context) error
}

type materializerStub struct {
	path     string
	err      error
	requests []*MaterializeOutputRequest
	mu       sync.Mutex
}

func (m *materializerStub) ReserveOutput(
	_ context.Context,
	request *ReserveOutputRequest,
) (string, error) {
	if m.path == "" {
		m.path = "/outputs/" + request.TaskID
	}
	return m.path, nil
}

func (m *materializerStub) AppendOutput(
	_ context.Context,
	request *MaterializeOutputRequest,
) error {
	m.mu.Lock()
	copy := *request
	copy.Data = append([]byte(nil), request.Data...)
	m.requests = append(m.requests, &copy)
	m.mu.Unlock()
	return m.err
}

func (r *fakeRun) Wait(ctx context.Context) (*Outcome, error) { return r.wait(ctx) }
func (r *fakeRun) Stop(ctx context.Context) error {
	if r.stop != nil {
		return r.stop(ctx)
	}
	return nil
}

type updatingRun struct {
	*fakeRun
	updates *schema.StreamReader[*Update]
}

func (r *updatingRun) Updates() *schema.StreamReader[*Update] { return r.updates }

type managedToolStartEventModel struct {
	mu    sync.Mutex
	calls int
}

func (m *managedToolStartEventModel) Generate(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 1 {
		return schema.AssistantMessage("call external", []schema.ToolCall{{
			ID: "call_start_event",
			Function: schema.FunctionCall{
				Name:      "external",
				Arguments: `{"value":"work"}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *managedToolStartEventModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *managedToolStartEventModel) WithTools(
	_ []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	return m, nil
}

type managedToolStartEventMarkerKey struct{}

type managedToolStartEventMarkerMiddleware struct {
	*adk.TypedBaseChatModelAgentMiddleware[*schema.Message]
}

func (m *managedToolStartEventMarkerMiddleware) WrapEnhancedInvokableToolCall(
	_ context.Context,
	endpoint adk.EnhancedInvokableToolCallEndpoint,
	_ *adk.ToolContext,
) (adk.EnhancedInvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argument *schema.ToolArgument, opts ...componenttool.Option) (*schema.ToolResult, error) {
		return endpoint(
			context.WithValue(ctx, managedToolStartEventMarkerKey{}, "tool-call-marker"),
			argument,
			opts...,
		)
	}, nil
}

func toolInfo(name string) *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: name, Desc: "Run external work",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"value": {Type: schema.String},
		}),
	}
}

func newTestManagedTool(
	t *testing.T,
	implementation Tool,
	timeout time.Duration,
) (*background.Manager, componenttool.BaseTool) {
	t.Helper()
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
		Description: func(string) string { return "External operation" },
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	})
	timeoutMs := int(timeout / time.Millisecond)
	config := &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
		SessionID:           func(context.Context) (string, error) { return "session", nil },
	}
	_, autoBackground := implementation.(*autoBackgroundFakeTool)
	if _, ok := implementation.(*autoBackgroundResumableFakeTool); ok {
		autoBackground = true
	}
	if autoBackground {
		config.ShouldAutoBackground = func(context.Context, *foreground.CandidateInfo) bool {
			return true
		}
	}
	wrapped, err := NewManagedTool(context.Background(), config)
	require.NoError(t, err)
	return manager, wrapped
}

func toolArgument(text string) *schema.ToolArgument {
	return &schema.ToolArgument{Text: text}
}

func decodeEvents(t *testing.T, results []*schema.ToolResult) []*ManagedToolResponseEvent {
	t.Helper()
	events := make([]*ManagedToolResponseEvent, 0, len(results))
	for _, result := range results {
		require.NotNil(t, result)
		require.NotEmpty(t, result.Parts)
		require.Equal(t, schema.ToolPartTypeText, result.Parts[0].Type)
		var event ManagedToolResponseEvent
		require.NoError(t, json.Unmarshal([]byte(result.Parts[0].Text), &event))
		events = append(events, &event)
	}
	return events
}

func waitTaskAttempt(t *testing.T, manager *background.Manager, taskID string) *background.TaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		task, err := manager.Get(context.Background(), taskID)
		require.NoError(t, err)
		if task.Attempt > 0 || task.Status != background.StatusPending {
			return task
		}
		require.True(t, time.Now().Before(deadline), "task was not claimed")
		time.Sleep(time.Millisecond)
	}
}

func waitTaskTerminal(t *testing.T, manager *background.Manager, taskID string) *background.TaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		task, err := manager.Get(context.Background(), taskID)
		require.NoError(t, err)
		if task.Status != background.StatusPending && task.Status != background.StatusRunning {
			return task
		}
		require.True(t, time.Now().Before(deadline), "task did not finish")
		time.Sleep(time.Millisecond)
	}
}

func waitForegroundMailboxState(
	t *testing.T,
	manager *background.Manager,
	taskID string,
	want taskcore.MailboxState,
) *taskcore.Mailbox {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		mailbox, err := manager.GetMailbox(context.Background(), taskID)
		if err == nil && mailbox.State == want {
			return mailbox
		}
		require.True(
			t,
			time.Now().Before(deadline),
			"mailbox did not reach %q: %v",
			want,
			err,
		)
		time.Sleep(time.Millisecond)
	}
}

type startFailStore struct {
	*background.InMemoryStore
	err     error
	started chan struct{}
	once    sync.Once
}

type mailboxFinalizationErrorStore struct {
	*background.InMemoryStore
	sealErr    error
	abandonErr error
}

func (s *mailboxFinalizationErrorStore) SealIfIdle(
	context.Context,
	*taskcore.SealMailboxRequest,
) (*taskcore.Mailbox, error) {
	return nil, s.sealErr
}

func (s *mailboxFinalizationErrorStore) Abandon(
	context.Context,
	*taskcore.AbandonMailboxRequest,
) (*taskcore.Mailbox, error) {
	return nil, s.abandonErr
}

func (s *startFailStore) Start(context.Context, *background.StartTaskRequest) (*background.TaskSnapshot, error) {
	s.once.Do(func() { close(s.started) })
	return nil, s.err
}

func TestManagedToolPreparesInputBeforeTaskCreation_BitsUT(t *testing.T) {
	prepareErr := errors.New("input preparation interrupted")
	implementation := &preparingPlainTool{
		plainFakeTool: &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				t.Fatal("Start must not run when input preparation fails")
				return nil, nil
			},
		},
		prepare: func(context.Context, string) (string, error) {
			return "", prepareErr
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)

	_, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"original"}`),
	)
	require.ErrorIs(t, err, prepareErr)
	_, err = manager.Get(context.Background(), "task-fixed")
	require.ErrorIs(t, err, background.ErrNotFound)

	reader, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"original"}`),
	)
	require.ErrorIs(t, err, prepareErr)
	require.Nil(t, reader)
}

func TestManagedToolInputPreparerSupportsTargetedResume_BitsUT(t *testing.T) {
	ctx := context.Background()
	implementation := &interruptingInputTool{}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: []componenttool.BaseTool{wrapped},
	})
	require.NoError(t, err)
	graph := compose.NewGraph[*schema.Message, []*schema.Message]()
	require.NoError(t, graph.AddToolsNode("tools", toolsNode))
	require.NoError(t, graph.AddEdge(compose.START, "tools"))
	require.NoError(t, graph.AddEdge("tools", compose.END))
	runnable, err := graph.Compile(
		ctx,
		compose.WithGraphName("managed_input_preparation"),
		compose.WithCheckPointStore(newManagedToolCheckpointStore()),
	)
	require.NoError(t, err)
	const checkpointID = "managed-input-preparation"
	input := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: "call_prepare",
			Function: schema.FunctionCall{
				Name: "external", Arguments: `{"value":"original"}`,
			},
		}},
	}

	_, err = runnable.Invoke(ctx, input, compose.WithCheckPointID(checkpointID))
	require.Error(t, err)
	interrupt, ok := compose.ExtractInterruptInfo(err)
	require.True(t, ok, "err: %v", err)
	require.Len(t, interrupt.InterruptContexts, 1)
	require.Equal(t, "Which region should be used?", interrupt.InterruptContexts[0].Info)
	_, err = manager.Get(ctx, "task-fixed")
	require.ErrorIs(t, err, background.ErrNotFound)
	require.Equal(t, 0, implementation.startCount())

	resumeCtx := compose.ResumeWithData(
		ctx,
		interrupt.InterruptContexts[0].ID,
		"us-east",
	)
	_, err = runnable.Invoke(
		resumeCtx,
		input,
		compose.WithCheckPointID(checkpointID),
	)
	require.NoError(t, err)
	require.Equal(t, 1, implementation.startCount())
	require.JSONEq(
		t,
		`{"value":"original","region":"us-east"}`,
		implementation.startedArguments(),
	)
	_, err = manager.Get(ctx, "task-fixed")
	require.ErrorIs(t, err, background.ErrNotFound)
}

type interruptingInputTool struct {
	mu        sync.Mutex
	starts    int
	arguments string
}

func (*interruptingInputTool) PrepareInput(
	ctx context.Context,
	arguments string,
) (string, error) {
	wasInterrupted, hasState, state := componenttool.GetInterruptState[map[string]string](ctx)
	if !wasInterrupted {
		return "", componenttool.StatefulInterrupt(
			ctx,
			"Which region should be used?",
			map[string]string{"arguments": arguments},
		)
	}
	if !hasState || state["arguments"] == "" {
		return "", errors.New("input preparation state is unavailable")
	}
	isTarget, hasData, region := componenttool.GetResumeContext[string](ctx)
	if !isTarget {
		return "", componenttool.StatefulInterrupt(ctx, nil, state)
	}
	if !hasData || region == "" {
		return "", errors.New("region is required")
	}
	var prepared map[string]any
	if err := json.Unmarshal([]byte(state["arguments"]), &prepared); err != nil {
		return "", err
	}
	prepared["region"] = region
	data, err := json.Marshal(prepared)
	return string(data), err
}

func (*interruptingInputTool) ValidateArguments(arguments string) error {
	var prepared struct {
		Value  string `json:"value"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal([]byte(arguments), &prepared); err != nil {
		return err
	}
	if prepared.Value == "" || prepared.Region == "" {
		return errors.New("value and region are required")
	}
	return nil
}

func (t *interruptingInputTool) Start(
	_ context.Context,
	request *StartRequest,
) (*StartResult, error) {
	t.mu.Lock()
	t.starts++
	t.arguments = request.Arguments
	t.mu.Unlock()
	return &StartResult{Run: &fakeRun{
		wait: func(context.Context) (*Outcome, error) {
			return &Outcome{Status: taskcore.OutcomeCompleted}, nil
		},
	}}, nil
}

func (t *interruptingInputTool) startCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.starts
}

func (t *interruptingInputTool) startedArguments() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.arguments
}

type managedToolCheckpointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newManagedToolCheckpointStore() *managedToolCheckpointStore {
	return &managedToolCheckpointStore{data: make(map[string][]byte)}
}

func (s *managedToolCheckpointStore) Get(
	_ context.Context,
	checkpointID string,
) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[checkpointID]
	return append([]byte(nil), value...), ok, nil
}

func (s *managedToolCheckpointStore) Set(
	_ context.Context,
	checkpointID string,
	value []byte,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[checkpointID] = append([]byte(nil), value...)
	return nil
}

func TestRegistrySnapshotsToolInfo(t *testing.T) {
	info := toolInfo("stable")
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: info,
		Tool: &plainFakeTool{start: func(context.Context, *StartRequest) (Run, error) {
			return nil, nil
		}},
	}))
	info.Name = "mutated"
	info.Desc = "mutated"
	registration, ok := registry.resolve("stable", false)
	require.True(t, ok)
	require.Equal(t, "stable", registration.Info.Name)
	require.Equal(t, "Run external work", registration.Info.Desc)
	_, ok = registry.resolve("mutated", false)
	require.False(t, ok)
}

func TestManagedToolInfoDescribesForegroundAndBackgroundResults(t *testing.T) {
	_, wrapped := newTestManagedTool(
		t,
		&plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return nil, nil
			},
		},
		time.Second,
	)

	info, err := wrapped.Info(context.Background())
	require.NoError(t, err)
	require.Equal(
		t,
		"Run external work\n"+
			"A published background handle returns a launch_result containing "+
			"an Eino task_id for task_output and task_stop. Synchronous completion "+
			"returns a foreground_result without a task_id, whether it came from "+
			"direct execution or an unpublished deferred task.",
		info.Desc,
	)
}

func TestManagedToolDirectForegroundCompletionOmitsTaskID(t *testing.T) {
	implementation := &fakeTool{
		start: func(_ context.Context, request *StartRequest) (Run, error) {
			require.Equal(t, "task-fixed", request.TaskID)
			return &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					return &Outcome{
						Status: taskcore.OutcomeCompleted,
						Data:   []byte(`{"answer":42}`),
					}, nil
				},
			}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"x"}`),
	)
	require.NoError(t, err)
	events := decodeEvents(t, []*schema.ToolResult{result})
	require.Equal(t, ManagedToolResponseEventForegroundResult, events[0].Type)
	require.Empty(t, events[0].TaskID)
	require.Equal(t, background.StatusCompleted, events[0].Status)
	require.Equal(t, map[string]any{"answer": float64(42)}, events[0].Output)
	waitForegroundMailboxState(
		t,
		manager,
		"task-fixed",
		taskcore.MailboxSealed,
	)

	_, err = manager.Get(context.Background(), "task-fixed")
	require.ErrorIs(t, err, background.ErrNotFound)
}

func TestManagedToolDirectCompletionPreservesPendingInput(t *testing.T) {
	newTool := func(t *testing.T) (*background.Manager, componenttool.BaseTool) {
		t.Helper()
		var manager *background.Manager
		implementation := &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(context.Context) (*Outcome, error) {
						_, err := manager.SendInput(
							context.Background(),
							&taskcore.SendInputRequest{
								TaskID: "task-fixed",
								Input: taskcore.Input{
									EventID: "pending",
									Kind:    "message",
									Data:    []byte("keep"),
								},
							},
						)
						if err != nil {
							return nil, err
						}
						return &Outcome{
							Status: taskcore.OutcomeCompleted,
							Data:   []byte(`{"answer":42}`),
						}, nil
					},
				}, nil
			},
		}
		manager, wrapped := newTestManagedTool(t, implementation, time.Second)
		return manager, wrapped
	}

	assertPending := func(t *testing.T, manager *background.Manager) {
		t.Helper()
		mailbox, err := manager.GetMailbox(context.Background(), "task-fixed")
		require.NoError(t, err)
		require.Equal(t, taskcore.MailboxForeground, mailbox.State)
		require.Equal(t, int64(1), mailbox.LatestSequence)
		require.Zero(t, mailbox.ConsumedCursor)
		inputs, err := manager.ListInputs(
			context.Background(),
			&taskcore.ListInputsRequest{
				TaskID: "task-fixed", AfterSequence: 0, Limit: 10,
			},
		)
		require.NoError(t, err)
		require.Len(t, inputs.Inputs, 1)
		require.Equal(t, "keep", string(inputs.Inputs[0].Data))
	}

	t.Run("buffered", func(t *testing.T) {
		manager, wrapped := newTool(t)
		result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.ErrorIs(t, err, taskcore.ErrInputsPending)
		require.NotNil(t, result)
		assertPending(t, manager)
	})

	t.Run("streaming", func(t *testing.T) {
		manager, wrapped := newTool(t)
		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		result, recvErr := stream.Recv()
		require.ErrorIs(t, recvErr, taskcore.ErrInputsPending)
		require.NotNil(t, result)
		assertPending(t, manager)
	})
}

func TestManagedToolDirectMailboxFinalizationErrorsAreReturned(t *testing.T) {
	newTool := func(
		t *testing.T,
		store *mailboxFinalizationErrorStore,
		implementation Tool,
	) componenttool.BaseTool {
		t.Helper()
		registry := NewRegistry()
		require.NoError(t, registry.Register(&Registration{
			Info: toolInfo("external"), Tool: implementation,
		}))
		manager := mustNewBackgroundManager(
			t,
			context.Background(),
			&background.Config{
				Tasks: store,
				IDGen: func(
					context.Context,
					*background.AllocateTaskIDRequest,
				) (string, error) {
					return "task-fixed", nil
				},
			},
		)
		timeoutMs := 0
		wrapped, err := NewManagedTool(
			context.Background(),
			&ManagedToolConfig{
				Manager: manager, Registry: registry, ToolName: "external",
				ForegroundTimeoutMs: &timeoutMs,
				SessionID: func(context.Context) (string, error) {
					return "session", nil
				},
			},
		)
		require.NoError(t, err)
		return wrapped
	}

	t.Run("seal", func(t *testing.T) {
		wantErr := errors.New("seal failed")
		wrapped := newTool(
			t,
			&mailboxFinalizationErrorStore{
				InMemoryStore: background.NewInMemoryStore(nil),
				sealErr:       wantErr,
			},
			&plainFakeTool{
				start: func(context.Context, *StartRequest) (Run, error) {
					return &fakeRun{
						wait: func(context.Context) (*Outcome, error) {
							return &Outcome{
								Status: taskcore.OutcomeCompleted,
								Data:   []byte(`{"answer":42}`),
							}, nil
						},
					}, nil
				},
			},
		)
		result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.ErrorIs(t, err, wantErr)
		require.NotNil(t, result)
	})

	t.Run("abandon", func(t *testing.T) {
		startErr := errors.New("start failed")
		abandonErr := errors.New("abandon failed")
		wrapped := newTool(
			t,
			&mailboxFinalizationErrorStore{
				InMemoryStore: background.NewInMemoryStore(nil),
				abandonErr:    abandonErr,
			},
			&plainFakeTool{
				start: func(context.Context, *StartRequest) (Run, error) {
					return nil, startErr
				},
			},
		)
		result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.Nil(t, result)
		require.ErrorIs(t, err, startErr)
		require.ErrorIs(t, err, abandonErr)
	})
}

func TestManagedToolDirectForegroundMailboxFailures(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		outcome    *Outcome
		wantStatus background.Status
		wantError  string
	}{
		{
			name: "failed outcome",
			outcome: &Outcome{
				Status: taskcore.OutcomeFailed,
				Error:  "operation failed",
			},
			wantStatus: background.StatusFailed,
			wantError:  "operation failed",
		},
		{
			name: "canceled outcome",
			outcome: &Outcome{
				Status: taskcore.OutcomeCanceled,
				Error:  "operation canceled",
			},
			wantStatus: background.StatusCanceled,
			wantError:  "operation canceled",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager, wrapped := newTestManagedTool(t, &plainFakeTool{
				start: func(context.Context, *StartRequest) (Run, error) {
					return &fakeRun{
						wait: func(context.Context) (*Outcome, error) {
							return testCase.outcome, nil
						},
					}, nil
				},
			}, time.Second)
			result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
				context.Background(),
				toolArgument(`{"value":"x"}`),
			)
			require.NoError(t, err)
			event := decodeEvents(t, []*schema.ToolResult{result})[0]
			require.Equal(t, testCase.wantStatus, event.Status)
			require.Equal(t, testCase.wantError, event.Error)
			waitForegroundMailboxState(
				t,
				manager,
				"task-fixed",
				taskcore.MailboxSealed,
			)
		})
	}

	t.Run("start failure", func(t *testing.T) {
		wantErr := errors.New("start failed")
		manager, wrapped := newTestManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return nil, wantErr
			},
		}, time.Second)
		result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.Nil(t, result)
		require.ErrorIs(t, err, wantErr)
		waitForegroundMailboxState(
			t,
			manager,
			"task-fixed",
			taskcore.MailboxSealed,
		)
	})

	t.Run("caller cancellation", func(t *testing.T) {
		started := make(chan struct{})
		manager, wrapped := newTestManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				close(started)
				return &fakeRun{
					wait: func(ctx context.Context) (*Outcome, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					},
				}, nil
			},
		}, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		returned := make(chan error, 1)
		go func() {
			_, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
				ctx,
				toolArgument(`{"value":"x"}`),
			)
			returned <- err
		}()
		<-started
		cancel()
		require.ErrorIs(t, <-returned, context.Canceled)
		waitForegroundMailboxState(
			t,
			manager,
			"task-fixed",
			taskcore.MailboxSealed,
		)
	})

	t.Run("timeout", func(t *testing.T) {
		manager, wrapped := newTestManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(ctx context.Context) (*Outcome, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					},
				}, nil
			},
		}, time.Millisecond)
		result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.Nil(t, result)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var timeoutErr *taskcore.ForegroundTimeoutError
		require.ErrorAs(t, err, &timeoutErr)
		require.Equal(t, time.Millisecond, timeoutErr.Timeout)
		require.Equal(t, "task-fixed", timeoutErr.TaskID)
		waitForegroundMailboxState(
			t,
			manager,
			"task-fixed",
			taskcore.MailboxSealed,
		)
	})

	t.Run("stream timeout", func(t *testing.T) {
		manager, wrapped := newTestManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(ctx context.Context) (*Outcome, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					},
				}, nil
			},
		}, time.Millisecond)
		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		result, err := stream.Recv()
		require.Nil(t, result)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var timeoutErr *taskcore.ForegroundTimeoutError
		require.ErrorAs(t, err, &timeoutErr)
		require.Equal(t, time.Millisecond, timeoutErr.Timeout)
		require.Equal(t, "task-fixed", timeoutErr.TaskID)
		waitForegroundMailboxState(
			t,
			manager,
			"task-fixed",
			taskcore.MailboxSealed,
		)
	})

	t.Run("stream error", func(t *testing.T) {
		wantErr := errors.New("stream failed")
		implementation := &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				updates, writer := schema.Pipe[*Update](1)
				go func() {
					writer.Send(nil, wantErr)
					writer.Close()
				}()
				return &updatingRun{
					fakeRun: &fakeRun{
						wait: func(ctx context.Context) (*Outcome, error) {
							<-ctx.Done()
							return nil, ctx.Err()
						},
					},
					updates: updates,
				}, nil
			},
		}
		manager, wrapped := newTestManagedTool(t, implementation, time.Second)
		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		defer stream.Close()
		_, err = stream.Recv()
		require.ErrorIs(t, err, wantErr)
		waitForegroundMailboxState(
			t,
			manager,
			"task-fixed",
			taskcore.MailboxSealed,
		)
	})

	t.Run("reader close", func(t *testing.T) {
		releaseUpdate := make(chan struct{})
		stopped := make(chan struct{})
		started := make(chan struct{})
		var stopOnce sync.Once
		implementation := &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				close(started)
				updates, writer := schema.Pipe[*Update](1)
				go func() {
					<-releaseUpdate
					writer.Send(&Update{Kind: "stdout", Data: []byte("late")}, nil)
					writer.Close()
				}()
				return &updatingRun{
					fakeRun: &fakeRun{
						wait: func(ctx context.Context) (*Outcome, error) {
							select {
							case <-stopped:
								return &Outcome{Status: taskcore.OutcomeCanceled}, nil
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						},
						stop: func(context.Context) error {
							stopOnce.Do(func() { close(stopped) })
							return nil
						},
					},
					updates: updates,
				}, nil
			},
		}
		manager, wrapped := newTestManagedTool(t, implementation, time.Second)
		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			context.Background(),
			toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		<-started
		stream.Close()
		close(releaseUpdate)
		waitForegroundMailboxState(
			t,
			manager,
			"task-fixed",
			taskcore.MailboxSealed,
		)
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("closing the reader did not stop direct foreground work")
		}
	})
}

func TestManagedToolStreamingErrorBoundaries(t *testing.T) {
	t.Run("foreground update error", func(t *testing.T) {
		updateErr := errors.New("update failed")
		stopped := make(chan struct{})
		manager, wrapped := newTestManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				reader, writer := schema.Pipe[*Update](1)
				writer.Send(nil, updateErr)
				writer.Close()
				return &updatingRun{
					updates: reader,
					fakeRun: &fakeRun{
						wait: func(ctx context.Context) (*Outcome, error) {
							<-ctx.Done()
							return nil, ctx.Err()
						},
						stop: func(context.Context) error {
							close(stopped)
							return nil
						},
					},
				}, nil
			},
		}, time.Second)

		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results, recvErr := readStreamResults(t, stream)
		require.Empty(t, results)
		require.ErrorIs(t, recvErr, updateErr)
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("update failure did not stop foreground work")
		}
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("foreground wait error", func(t *testing.T) {
		waitErr := errors.New("wait failed")
		manager, wrapped := newTestManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{wait: func(context.Context) (*Outcome, error) {
					return nil, waitErr
				}}, nil
			},
		}, time.Second)

		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results := readAllStreamResults(t, stream)
		require.Len(t, results, 1)
		event := decodeEvents(t, results)[0]
		require.Equal(t, ManagedToolResponseEventForegroundResult, event.Type)
		require.Equal(t, background.StatusFailed, event.Status)
		require.Equal(t, waitErr.Error(), event.Error)
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("task-first update error", func(t *testing.T) {
		updateErr := errors.New("task-first update failed")
		implementation := &autoBackgroundFakeTool{fakeTool: &fakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				reader, writer := schema.Pipe[*Update](1)
				writer.Send(nil, updateErr)
				writer.Close()
				return &updatingRun{
					updates: reader,
					fakeRun: &fakeRun{wait: func(ctx context.Context) (*Outcome, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					}},
				}, nil
			},
		}}
		manager, wrapped := newTestManagedTool(t, implementation, time.Second)

		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results := readAllStreamResults(t, stream)
		require.Len(t, results, 1)
		event := decodeEvents(t, results)[0]
		require.Equal(t, ManagedToolResponseEventForegroundResult, event.Type)
		require.Equal(t, background.StatusFailed, event.Status)
		require.Contains(t, event.Error, updateErr.Error())
		failed := waitTaskTerminal(t, manager, "task-fixed")
		require.Equal(t, background.StatusFailed, failed.Status)
		require.Contains(t, failed.ResultError, updateErr.Error())
	})
}

func TestManagedToolTaskFirstForegroundCompletionStaysDeferred(t *testing.T) {
	implementation := &autoBackgroundFakeTool{fakeTool: &fakeTool{
		start: func(_ context.Context, request *StartRequest) (Run, error) {
			require.Equal(t, int64(1), request.Attempt)
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{
					Status: taskcore.OutcomeCompleted,
					Data:   []byte(`{"answer":42}`),
				}, nil
			}}, nil
		},
	}}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(),
		toolArgument(`{"value":"x"}`),
	)
	require.NoError(t, err)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, ManagedToolResponseEventForegroundResult, event.Type)
	require.Empty(t, event.TaskID)
	require.Equal(t, background.StatusCompleted, event.Status)

	task, err := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, task.Status)
	require.Equal(t, background.PublicationDeferred, task.Publication)
}

func TestManagedToolForegroundStartCanSendParentSessionEvent(t *testing.T) {
	ctx := context.Background()
	const parentSessionID = "managed-tool-start-parent"
	startEventKind := adk.SessionEventKind(adk.SessionEventExtensionPrefix + "managed_tool.start")
	implementation := &fakeTool{
		start: func(ctx context.Context, request *StartRequest) (Run, error) {
			require.Equal(t, int64(0), request.Attempt)
			require.Equal(t, "task-fixed", request.TaskID)
			err := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[*schema.Message]{
				SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
					Event: &adk.SessionEvent[*schema.Message]{
						Kind:      startEventKind,
						Extension: &adk.SessionExtensionEvent{},
					},
				},
			})
			require.NoError(t, err)
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{
					Status: taskcore.OutcomeCompleted,
					Data:   []byte(`{"ok":true}`),
				}, nil
			}}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info:        toolInfo("external"),
		Tool:        implementation,
		Description: func(string) string { return "External operation" },
	}))
	manager := mustNewBackgroundManager(t, ctx, &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	})
	timeoutMs := int(time.Second / time.Millisecond)
	wrapped, err := NewManagedTool(ctx, &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
	})
	require.NoError(t, err)
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "managed-tool-start-event-agent",
		Instruction: "call external once",
		Model:       &managedToolStartEventModel{},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{wrapped},
			},
		},
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent: agent, SessionID: parentSessionID, SessionStore: sessionStore,
	})

	iterator := runner.Query(ctx, "start external work")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}

	result, err := sessionStore.LoadEvents(ctx, parentSessionID, &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{startEventKind},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	require.Equal(t, startEventKind, result.Events[0].Kind)
	require.NotEmpty(t, result.Events[0].EventID)
	require.NotEmpty(t, result.Events[0].TurnID)
	require.NotNil(t, result.Events[0].Extension)
}

func TestManagedToolBackgroundStartCanSendParentSessionEvent(t *testing.T) {
	ctx := context.Background()
	const parentSessionID = "managed-tool-background-start-parent"
	startEventKind := adk.SessionEventKind(adk.SessionEventExtensionPrefix + "managed_tool.background_start")
	implementation := &fakeTool{
		start: func(ctx context.Context, request *StartRequest) (Run, error) {
			require.Equal(t, int64(1), request.Attempt)
			require.Equal(t, "task-fixed", request.TaskID)
			require.Nil(t, ctx.Value(managedToolStartEventMarkerKey{}))
			err := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[*schema.Message]{
				SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
					Event: &adk.SessionEvent[*schema.Message]{
						Kind:      startEventKind,
						Extension: &adk.SessionExtensionEvent{},
					},
				},
			})
			require.NoError(t, err)
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info:        toolInfo("external"),
		Tool:        implementation,
		Description: func(string) string { return "External operation" },
	}))
	manager := mustNewBackgroundManager(t, ctx, &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	})
	timeoutMs := int(time.Second / time.Millisecond)
	wrapped, err := NewManagedTool(ctx, &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
		RunInBackground:     func(context.Context, string) bool { return true },
	})
	require.NoError(t, err)
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "managed-tool-background-start-event-agent",
		Instruction: "call external once",
		Model:       &managedToolStartEventModel{},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{wrapped},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{
			&managedToolStartEventMarkerMiddleware{
				TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[*schema.Message]{},
			},
		},
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent: agent, SessionID: parentSessionID, SessionStore: sessionStore,
		SessionConfig: &adk.SessionConfig[*schema.Message]{
			EventIDGenerator: func(ctx context.Context, event *adk.SessionEvent[*schema.Message]) (string, error) {
				if event.Kind == startEventKind {
					return "start-event-id", nil
				}
				return adk.DefaultSessionEventIDGenerator[*schema.Message](ctx, event)
			},
			EventExtraProvider: func(ctx context.Context, _ *adk.SessionEvent[*schema.Message]) (map[string]any, error) {
				marker, _ := ctx.Value(managedToolStartEventMarkerKey{}).(string)
				if marker == "" {
					return nil, nil
				}
				return map[string]any{"marker": marker}, nil
			},
		},
	})

	iterator := runner.Query(ctx, "start external work")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}

	result, err := sessionStore.LoadEvents(ctx, parentSessionID, &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{startEventKind},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	require.Equal(t, "start-event-id", result.Events[0].EventID)
	require.NotEmpty(t, result.Events[0].TurnID)
	require.Equal(t, "tool-call-marker", result.Events[0].Extra["marker"])
	_, err = manager.Get(ctx, "task-fixed")
	require.NoError(t, err)
}

func TestManagedToolBackgroundWaitCannotSendParentSessionEvent(t *testing.T) {
	ctx := context.Background()
	const parentSessionID = "managed-tool-background-wait-parent"
	waitEventKind := adk.SessionEventKind(adk.SessionEventExtensionPrefix + "managed_tool.wait")
	waitSent := make(chan error, 1)
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{wait: func(ctx context.Context) (*Outcome, error) {
				waitSent <- adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[*schema.Message]{
					SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
						Event: &adk.SessionEvent[*schema.Message]{
							Kind:      waitEventKind,
							Extension: &adk.SessionExtensionEvent{},
						},
					},
				})
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info:        toolInfo("external"),
		Tool:        implementation,
		Description: func(string) string { return "External operation" },
	}))
	manager := mustNewBackgroundManager(t, ctx, &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	})
	timeoutMs := int(time.Second / time.Millisecond)
	wrapped, err := NewManagedTool(ctx, &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
		RunInBackground:     func(context.Context, string) bool { return true },
	})
	require.NoError(t, err)
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "managed-tool-background-wait-event-agent",
		Instruction: "call external once",
		Model:       &managedToolStartEventModel{},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{wrapped},
			},
		},
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent: agent, SessionID: parentSessionID, SessionStore: sessionStore,
	})

	iterator := runner.Query(ctx, "start external work")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	require.ErrorIs(t, <-waitSent, adk.ErrStartWindowClosed)
	result, err := sessionStore.LoadEvents(ctx, parentSessionID, &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{waitEventKind},
	})
	require.NoError(t, err)
	require.Empty(t, result.Events)
}

func TestManagedToolBackgroundStartWindowDoesNotHangOnPreExecuteFailure(t *testing.T) {
	startErr := errors.New("claim unavailable")
	store := &startFailStore{
		InMemoryStore: background.NewInMemoryStore(nil),
		err:           startErr,
		started:       make(chan struct{}),
	}
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			t.Fatal("Start must not run when TaskStore.Start fails")
			return nil, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store, IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "pre-execute-failure", nil
		},
	})
	timeoutMs := int(time.Second / time.Millisecond)
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
		RunInBackground:     func(context.Context, string) bool { return true },
		SessionID:           func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)

	done := make(chan struct {
		result *schema.ToolResult
		err    error
	}, 1)
	go func() {
		result, runErr := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		done <- struct {
			result *schema.ToolResult
			err    error
		}{result: result, err: runErr}
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("TaskStore.Start was not reached")
	}
	select {
	case received := <-done:
		require.NoError(t, received.err)
		event := decodeEvents(t, []*schema.ToolResult{received.result})[0]
		require.Equal(t, ManagedToolResponseEventLaunchResult, event.Type)
		require.Equal(t, "pre-execute-failure", event.TaskID)
	case <-time.After(time.Second):
		t.Fatal("background launch hung after pre-execute failure")
	}
}

func TestManagedToolBackgroundStartWindowTimeoutReturnsLaunchResult(t *testing.T) {
	ctx := context.Background()
	const parentSessionID = "managed-tool-background-timeout-parent"
	startEventKind := adk.SessionEventKind(adk.SessionEventExtensionPrefix + "managed_tool.timeout")
	startEntered := make(chan struct{})
	continueStart := make(chan struct{})
	sendErr := make(chan error, 1)
	implementation := &fakeTool{
		start: func(ctx context.Context, request *StartRequest) (Run, error) {
			require.Equal(t, int64(1), request.Attempt)
			close(startEntered)
			<-continueStart
			sendErr <- adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[*schema.Message]{
				SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
					Event: &adk.SessionEvent[*schema.Message]{
						Kind:      startEventKind,
						Extension: &adk.SessionExtensionEvent{},
					},
				},
			})
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info:        toolInfo("external"),
		Tool:        implementation,
		Description: func(string) string { return "External operation" },
	}))
	manager := mustNewBackgroundManager(t, ctx, &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	})
	timeoutMs := 50
	wrapped, err := NewManagedTool(ctx, &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
		RunInBackground:     func(context.Context, string) bool { return true },
	})
	require.NoError(t, err)
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "managed-tool-background-timeout-agent",
		Instruction: "call external once",
		Model:       &managedToolStartEventModel{},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{wrapped},
			},
		},
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent: agent, SessionID: parentSessionID, SessionStore: sessionStore,
	})

	startedAt := time.Now()
	iterator := runner.Query(ctx, "start external work")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	elapsed := time.Since(startedAt)
	require.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	require.Less(t, elapsed, time.Second)
	select {
	case <-startEntered:
	default:
		t.Fatal("Start was not reached before the launch result returned")
	}
	task, err := manager.Get(ctx, "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusRunning, task.Status)
	close(continueStart)
	require.ErrorIs(t, <-sendErr, adk.ErrStartWindowClosed)
	terminal := waitTaskTerminal(t, manager, "task-fixed")
	require.Equal(t, background.StatusCompleted, terminal.Status)

	result, err := sessionStore.LoadEvents(ctx, parentSessionID, &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{startEventKind},
	})
	require.NoError(t, err)
	require.Empty(t, result.Events)
}

func TestManagedToolBackgroundStartWindowWithoutSenderStillWaits(t *testing.T) {
	startEntered := make(chan struct{})
	continueStart := make(chan struct{})
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			close(startEntered)
			<-continueStart
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	wrapped.(*managedTool).runInBackground = func(context.Context, string) bool { return true }
	wrapped.(*managedTool).policy.TimeoutMs = 40
	t.Cleanup(func() { close(continueStart) })

	startedAt := time.Now()
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"x"}`),
	)
	require.NoError(t, err)
	elapsed := time.Since(startedAt)
	require.GreaterOrEqual(t, elapsed, 30*time.Millisecond)
	require.Less(t, elapsed, time.Second)
	select {
	case <-startEntered:
	default:
		t.Fatal("Start was not reached")
	}
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, ManagedToolResponseEventLaunchResult, event.Type)
	require.Equal(t, "task-fixed", event.TaskID)
	task, err := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusRunning, task.Status)
	require.Equal(t, background.PublicationOnCreate, task.Publication)
}

func TestManagedToolBackgroundStreamReturnsLaunchBeforeTaskTerminal(t *testing.T) {
	waitReleased := make(chan struct{})
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				<-waitReleased
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	wrapped.(*managedTool).runInBackground = func(context.Context, string) bool { return true }
	t.Cleanup(func() { close(waitReleased) })

	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"stream"}`),
	)
	require.NoError(t, err)
	defer stream.Close()
	result, recvErr := stream.Recv()
	require.NoError(t, recvErr)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, ManagedToolResponseEventLaunchResult, event.Type)
	require.Equal(t, "task-fixed", event.TaskID)
	task, err := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusRunning, task.Status)
}

func TestAttack_BackgroundStreamParentCancelReturnsLaunchResult(t *testing.T) {
	startEntered := make(chan struct{})
	continueStart := make(chan struct{})
	var releaseOnce sync.Once
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			close(startEntered)
			<-continueStart
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	wrapped.(*managedTool).runInBackground = func(context.Context, string) bool { return true }
	t.Cleanup(func() { releaseOnce.Do(func() { close(continueStart) }) })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		stream *schema.StreamReader[*schema.ToolResult]
		err    error
	}, 1)
	go func() {
		stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
			ctx, toolArgument(`{"value":"stream"}`),
		)
		done <- struct {
			stream *schema.StreamReader[*schema.ToolResult]
			err    error
		}{stream: stream, err: err}
	}()
	select {
	case <-startEntered:
	case <-time.After(time.Second):
		t.Fatal("Start was not reached")
	}
	cancel()
	var received struct {
		stream *schema.StreamReader[*schema.ToolResult]
		err    error
	}
	select {
	case received = <-done:
	case <-time.After(time.Second):
		t.Fatal("StreamableRun did not return after parent cancellation")
	}
	require.NoError(t, received.err)
	defer received.stream.Close()
	result, recvErr := received.stream.Recv()
	require.NoError(t, recvErr)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, ManagedToolResponseEventLaunchResult, event.Type)
	require.Equal(t, "task-fixed", event.TaskID)
	task, err := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusRunning, task.Status)
	releaseOnce.Do(func() { close(continueStart) })
}

func TestManagedToolBackgroundStreamProjectionHandlesUpdatePressure(t *testing.T) {
	waitReleased := make(chan struct{})
	updates := make([]*Update, 0, projectionBuffer+8)
	for i := 0; i < projectionBuffer+8; i++ {
		updates = append(updates, &Update{
			EventID: fmt.Sprintf("event-%d", i),
			Kind:    "stdout",
			Data:    []byte(fmt.Sprintf("line-%d", i)),
		})
	}
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &updatingRun{
				fakeRun: &fakeRun{wait: func(context.Context) (*Outcome, error) {
					<-waitReleased
					return &Outcome{Status: taskcore.OutcomeCompleted}, nil
				}},
				updates: schema.StreamReaderFromArray(updates),
			}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	wrapped.(*managedTool).runInBackground = func(context.Context, string) bool { return true }
	t.Cleanup(func() { close(waitReleased) })

	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"stream"}`),
	)
	require.NoError(t, err)
	events := decodeEvents(t, readAllStreamResults(t, stream))
	require.NotEmpty(t, events)
	require.Equal(t, ManagedToolResponseEventLaunchResult, events[len(events)-1].Type)
	require.Equal(t, "task-fixed", events[len(events)-1].TaskID)
	task, err := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusRunning, task.Status)
}

func TestManagedToolBackgroundStartWindowStartErrorReturnsLaunchResult(t *testing.T) {
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return nil, errors.New("start failed")
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	wrapped.(*managedTool).runInBackground = func(context.Context, string) bool { return true }

	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"x"}`),
	)
	require.NoError(t, err)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, ManagedToolResponseEventLaunchResult, event.Type)
	require.Equal(t, "task-fixed", event.TaskID)
	task := waitTaskTerminal(t, manager, "task-fixed")
	require.Equal(t, background.StatusFailed, task.Status)
	require.Contains(t, task.ResultError, "start failed")
}

func TestManagedToolBackgroundStartWindowIgnoresForegroundTimeoutOverride(t *testing.T) {
	ctx := context.Background()
	const parentSessionID = "managed-tool-background-foreground-timeout-parent"
	startEventKind := adk.SessionEventKind(adk.SessionEventExtensionPrefix + "managed_tool.foreground_timeout")
	implementation := &fakeTool{
		start: func(ctx context.Context, request *StartRequest) (Run, error) {
			require.Equal(t, int64(1), request.Attempt)
			time.Sleep(40 * time.Millisecond)
			err := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[*schema.Message]{
				SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
					Event: &adk.SessionEvent[*schema.Message]{
						Kind:      startEventKind,
						Extension: &adk.SessionExtensionEvent{},
					},
				},
			})
			require.NoError(t, err)
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info:        toolInfo("external"),
		Tool:        implementation,
		Description: func(string) string { return "External operation" },
	}))
	manager := mustNewBackgroundManager(t, ctx, &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	})
	foregroundTimeoutMs := 200
	foregroundTimeoutOverrideMs := 10
	wrapped, err := NewManagedTool(ctx, &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &foregroundTimeoutMs,
		RunInBackground:     func(context.Context, string) bool { return true },
		ForegroundTimeoutMsForInvocation: func(context.Context, string) *int {
			return &foregroundTimeoutOverrideMs
		},
	})
	require.NoError(t, err)
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "managed-tool-background-foreground-timeout-agent",
		Instruction: "call external once",
		Model:       &managedToolStartEventModel{},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{wrapped},
			},
		},
	})
	require.NoError(t, err)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent: agent, SessionID: parentSessionID, SessionStore: sessionStore,
	})

	iterator := runner.Query(ctx, "start external work")
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}
	result, err := sessionStore.LoadEvents(ctx, parentSessionID, &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{startEventKind},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
}

func TestManagedToolForegroundWaitingInputResumesWithoutTask(t *testing.T) {
	ctx := context.Background()
	var (
		mu             sync.Mutex
		startRequests  []*StartRequest
		resumeRequests []*ResumeRequest
	)
	implementation := &resumableFakeTool{
		fakeTool: &fakeTool{
			startCheckpoint: []byte(`{"run_id":"foreground"}`),
			start: func(_ context.Context, request *StartRequest) (Run, error) {
				mu.Lock()
				copy := *request
				startRequests = append(startRequests, &copy)
				mu.Unlock()
				return &fakeRun{
					wait: func(context.Context) (*Outcome, error) {
						return &Outcome{
							Status: taskcore.OutcomeInterrupted,
							InputRequest: &InputRequest{
								ID: "approval", Data: []byte(`{"question":"Approve?"}`),
							},
						}, nil
					},
				}, nil
			},
			recover: func(context.Context, *RecoverRequest) (Run, error) {
				t.Fatal("foreground resume must not use background recovery")
				return nil, nil
			},
		},
		resume: func(_ context.Context, request *ResumeRequest) (Run, error) {
			mu.Lock()
			copy := *request
			copy.Data = append([]byte(nil), request.Data...)
			copy.Checkpoint = append([]byte(nil), request.Checkpoint...)
			resumeRequests = append(resumeRequests, &copy)
			mu.Unlock()
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{
					Status: taskcore.OutcomeCompleted,
					Data:   []byte(`{"approved":true}`),
				}, nil
			}}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: []componenttool.BaseTool{wrapped},
	})
	require.NoError(t, err)
	graph := compose.NewGraph[*schema.Message, []*schema.Message]()
	require.NoError(t, graph.AddToolsNode("tools", toolsNode))
	require.NoError(t, graph.AddEdge(compose.START, "tools"))
	require.NoError(t, graph.AddEdge("tools", compose.END))
	runnable, err := graph.Compile(
		ctx,
		compose.WithGraphName("managed_tool_foreground_wait"),
		compose.WithCheckPointStore(newManagedToolCheckpointStore()),
	)
	require.NoError(t, err)
	input := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: "call_wait",
			Function: schema.FunctionCall{
				Name: "external", Arguments: `{"value":"work"}`,
			},
		}},
	}

	_, err = runnable.Invoke(ctx, input, compose.WithCheckPointID("foreground-wait"))
	require.Error(t, err)
	interrupt, ok := compose.ExtractInterruptInfo(err)
	require.True(t, ok)
	require.Len(t, interrupt.InterruptContexts, 1)
	require.JSONEq(t, `{"question":"Approve?"}`, string(interrupt.InterruptContexts[0].Info.(json.RawMessage)))
	_, err = manager.Get(ctx, "task-fixed")
	require.ErrorIs(t, err, background.ErrNotFound)
	mailbox, err := manager.GetMailbox(ctx, "task-fixed")
	require.NoError(t, err)
	require.Equal(t, taskcore.MailboxForeground, mailbox.State)

	resumeCtx := compose.ResumeWithData(
		ctx,
		interrupt.InterruptContexts[0].ID,
		json.RawMessage(`"yes"`),
	)
	_, err = runnable.Invoke(
		resumeCtx,
		input,
		compose.WithCheckPointID("foreground-wait"),
	)
	require.NoError(t, err)
	_, err = manager.Get(ctx, "task-fixed")
	require.ErrorIs(t, err, background.ErrNotFound)
	waitForegroundMailboxState(
		t,
		manager,
		"task-fixed",
		taskcore.MailboxSealed,
	)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, startRequests, 1)
	require.Equal(t, int64(0), startRequests[0].Attempt)
	require.Len(t, resumeRequests, 1)
	require.Equal(t, int64(0), resumeRequests[0].Attempt)
	require.Equal(t, "task-fixed", resumeRequests[0].TaskID)
	require.Equal(t, "approval", resumeRequests[0].RequestID)
	require.Equal(t, `"yes"`, string(resumeRequests[0].Data))
	require.JSONEq(t, `{"run_id":"foreground"}`, string(resumeRequests[0].Checkpoint))
}

func TestManagedToolTaskFirstWaitingInputResumesSameTask(t *testing.T) {
	ctx := context.Background()
	var (
		mu             sync.Mutex
		startRequests  []*StartRequest
		resumeRequests []*ResumeRequest
	)
	implementation := &autoBackgroundFakeTool{fakeTool: &fakeTool{
		startCheckpoint: []byte(`{"run_id":"task-first"}`),
		start: func(_ context.Context, request *StartRequest) (Run, error) {
			mu.Lock()
			copy := *request
			startRequests = append(startRequests, &copy)
			mu.Unlock()
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{
					Status: taskcore.OutcomeInterrupted,
					InputRequest: &InputRequest{
						ID: "approval", Data: []byte(`{"question":"Approve?"}`),
					},
				}, nil
			}}, nil
		},
		recover: func(context.Context, *RecoverRequest) (Run, error) {
			t.Fatal("resume input must not use ordinary recovery")
			return nil, nil
		},
	}}
	resumable := &resumableFakeTool{
		fakeTool: implementation.fakeTool,
		resume: func(_ context.Context, request *ResumeRequest) (Run, error) {
			mu.Lock()
			copy := *request
			copy.Data = append([]byte(nil), request.Data...)
			copy.Checkpoint = append([]byte(nil), request.Checkpoint...)
			resumeRequests = append(resumeRequests, &copy)
			mu.Unlock()
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{
					Status: taskcore.OutcomeCompleted,
					Data:   []byte(`{"approved":true}`),
				}, nil
			}}, nil
		},
	}
	manager, wrapped := newTestManagedTool(
		t,
		&autoBackgroundResumableFakeTool{resumableFakeTool: resumable},
		time.Second,
	)

	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: []componenttool.BaseTool{wrapped},
	})
	require.NoError(t, err)
	graph := compose.NewGraph[*schema.Message, []*schema.Message]()
	require.NoError(t, graph.AddToolsNode("tools", toolsNode))
	require.NoError(t, graph.AddEdge(compose.START, "tools"))
	require.NoError(t, graph.AddEdge("tools", compose.END))
	runnable, err := graph.Compile(
		ctx,
		compose.WithGraphName("managed_tool_task_first_wait"),
		compose.WithCheckPointStore(newManagedToolCheckpointStore()),
	)
	require.NoError(t, err)
	input := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: "call_wait",
			Function: schema.FunctionCall{
				Name: "external", Arguments: `{"value":"work"}`,
			},
		}},
	}

	_, err = runnable.Invoke(ctx, input, compose.WithCheckPointID("task-first-wait"))
	require.Error(t, err)
	interrupt, ok := compose.ExtractInterruptInfo(err)
	require.True(t, ok)
	require.Len(t, interrupt.InterruptContexts, 1)
	waiting, err := manager.Get(ctx, "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusWaitingInput, waiting.Status)
	require.Equal(t, background.PublicationDeferred, waiting.Publication)

	resumeCtx := compose.ResumeWithData(
		ctx,
		interrupt.InterruptContexts[0].ID,
		json.RawMessage(`"yes"`),
	)
	_, err = runnable.Invoke(
		resumeCtx,
		input,
		compose.WithCheckPointID("task-first-wait"),
	)
	require.NoError(t, err)
	completed, err := manager.Get(ctx, "task-fixed")
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, completed.Status)
	require.Equal(t, background.PublicationDeferred, completed.Publication)
	require.Equal(t, int64(2), completed.Attempt)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, startRequests, 1)
	require.Equal(t, int64(1), startRequests[0].Attempt)
	require.Len(t, resumeRequests, 1)
	require.Equal(t, int64(2), resumeRequests[0].Attempt)
	require.Equal(t, "task-fixed", resumeRequests[0].TaskID)
	require.Equal(t, "approval", resumeRequests[0].RequestID)
	require.Equal(t, `"yes"`, string(resumeRequests[0].Data))
	require.JSONEq(t, `{"run_id":"task-first"}`, string(resumeRequests[0].Checkpoint))
}

func TestManagedToolStreamingInterruptResume(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		taskFirst bool
	}{
		{name: "foreground", taskFirst: false},
		{name: "task-first", taskFirst: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			var (
				mu             sync.Mutex
				startRequests  []*StartRequest
				resumeRequests []*ResumeRequest
			)
			resumable := &resumableFakeTool{
				fakeTool: &fakeTool{
					startCheckpoint: []byte(`{"run_id":"stream"}`),
					start: func(_ context.Context, request *StartRequest) (Run, error) {
						mu.Lock()
						copy := *request
						startRequests = append(startRequests, &copy)
						mu.Unlock()
						return &fakeRun{wait: func(context.Context) (*Outcome, error) {
							return &Outcome{
								Status: taskcore.OutcomeInterrupted,
								InputRequest: &InputRequest{
									ID:   "approval",
									Data: []byte(`{"question":"Approve stream?"}`),
								},
							}, nil
						}}, nil
					},
					recover: func(context.Context, *RecoverRequest) (Run, error) {
						t.Fatal("streaming interrupt resume must not use recovery")
						return nil, nil
					},
				},
				resume: func(_ context.Context, request *ResumeRequest) (Run, error) {
					mu.Lock()
					copy := *request
					copy.Data = append([]byte(nil), request.Data...)
					copy.Checkpoint = append([]byte(nil), request.Checkpoint...)
					resumeRequests = append(resumeRequests, &copy)
					mu.Unlock()
					return &fakeRun{wait: func(context.Context) (*Outcome, error) {
						return &Outcome{
							Status: taskcore.OutcomeCompleted,
							Data:   []byte(`{"approved":true}`),
						}, nil
					}}, nil
				},
			}
			var implementation Tool = resumable
			if testCase.taskFirst {
				implementation = &autoBackgroundResumableFakeTool{
					resumableFakeTool: resumable,
				}
			}
			manager, wrapped := newTestManagedTool(t, implementation, time.Second)
			callCtx := core.AppendAddressSegment(
				ctx, compose.AddressSegmentTool, "external", "call_stream_wait",
			)
			initialStream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
				callCtx, toolArgument(`{"value":"work"}`),
			)
			require.NoError(t, err)
			initialResults, interruptErr := readStreamResults(t, initialStream)
			require.Empty(t, initialResults)
			require.Error(t, interruptErr)
			var interrupt *core.InterruptSignal
			require.ErrorAs(t, interruptErr, &interrupt)
			require.JSONEq(
				t,
				`{"question":"Approve stream?"}`,
				string(interrupt.InterruptInfo.Info.(json.RawMessage)),
			)

			idToAddress, idToState := core.SignalToPersistenceMaps(interrupt)
			resumeCtx := compose.ResumeWithData(
				ctx,
				interrupt.ID,
				json.RawMessage(`"yes"`),
			)
			resumeCtx = core.AppendAddressSegment(
				resumeCtx, compose.AddressSegmentTool, "external", "call_stream_wait",
			)
			resumeCtx = core.PopulateInterruptState(
				resumeCtx,
				idToAddress,
				idToState,
			)
			stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
				resumeCtx,
				toolArgument(`{"value":"work"}`),
			)
			require.NoError(t, err)
			readAllStreamResults(t, stream)

			mu.Lock()
			require.Len(t, startRequests, 1)
			require.Len(t, resumeRequests, 1)
			require.Equal(t, "approval", resumeRequests[0].RequestID)
			require.Equal(t, `"yes"`, string(resumeRequests[0].Data))
			require.JSONEq(t, `{"run_id":"stream"}`, string(resumeRequests[0].Checkpoint))
			if testCase.taskFirst {
				require.Equal(t, int64(1), startRequests[0].Attempt)
				require.Equal(t, int64(2), resumeRequests[0].Attempt)
			} else {
				require.Zero(t, startRequests[0].Attempt)
				require.Zero(t, resumeRequests[0].Attempt)
			}
			mu.Unlock()

			if testCase.taskFirst {
				completed := waitTaskTerminal(t, manager, "task-fixed")
				require.Equal(t, background.StatusCompleted, completed.Status)
				require.Equal(t, background.PublicationDeferred, completed.Publication)
			} else {
				_, err = manager.Get(ctx, "task-fixed")
				require.ErrorIs(t, err, background.ErrNotFound)
				waitForegroundMailboxState(
					t, manager, "task-fixed", taskcore.MailboxSealed,
				)
			}
		})
	}
}

func TestManagedToolDurableInputResume_BitsUT(t *testing.T) {
	var mu sync.Mutex
	var resumeRequests []*ResumeRequest
	implementation := &resumableFakeTool{
		fakeTool: &fakeTool{
			startCheckpoint: []byte(`{"run_id":"input-run","stage":"approval"}`),
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(context.Context) (*Outcome, error) {
						return &Outcome{
							Status: taskcore.OutcomeInterrupted,
							InputRequest: &InputRequest{
								ID: "approval", Data: []byte(`{"question":"Approve?"}`),
							},
						}, nil
					},
				}, nil
			},
			recover: func(context.Context, *RecoverRequest) (Run, error) {
				t.Fatal("ordinary recovery must not consume resume input")
				return nil, nil
			},
		},
		resume: func(_ context.Context, request *ResumeRequest) (Run, error) {
			expectedCheckpoint := `{"run_id":"input-run","stage":"approval"}`
			if request.RequestID == "region" {
				expectedCheckpoint = `{"run_id":"input-run","stage":"region"}`
			}
			require.JSONEq(
				t,
				expectedCheckpoint,
				string(request.Checkpoint),
			)
			if request.RequestID == "approval" && string(request.Data) != "approve" {
				return nil, fmt.Errorf(
					"%w: approval must be explicit",
					ErrResumeInputRejected,
				)
			}
			mu.Lock()
			copy := *request
			copy.Data = append([]byte(nil), request.Data...)
			copy.Checkpoint = append([]byte(nil), request.Checkpoint...)
			resumeRequests = append(resumeRequests, &copy)
			mu.Unlock()
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				if request.RequestID == "approval" {
					return &Outcome{
						Status: taskcore.OutcomeInterrupted,
						InputRequest: &InputRequest{
							ID: "region", Data: []byte(`{"question":"Which region?"}`),
						},
						Checkpoint: []byte(
							`{"run_id":"input-run","stage":"region"}`,
						),
					}, nil
				}
				return &Outcome{
					Status: taskcore.OutcomeCompleted, Data: []byte("done"),
				}, nil
			}}, nil
		},
	}
	store := background.NewInMemoryStore(nil)
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store})
	require.NoError(t, registerExecutors(manager, registry))
	task, err := manager.Submit(context.Background(), &background.SubmitRequest{
		Spec: background.Spec{
			ID: "durable-input", ExecutorKey: RecoverableExecutorKey,
			Kind:    "background_tool",
			Payload: encodedPayload(t, "external", `{"value":"work"}`),
		},
	})
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusWaitingInput, waiting.Status)
	input, err := ReadInputRequest(waiting)
	require.NoError(t, err)
	require.Equal(t, "approval", input.ID)
	require.JSONEq(t, `{"question":"Approve?"}`, string(input.Data))
	rendered, err := (&managedTool{registration: &Registration{}}).
		renderLaunchResult(context.Background(), waiting)
	require.NoError(t, err)
	require.Contains(t, rendered.Parts[0].Text, `"data":{"question":"Approve?"}`)
	response := decodeEvents(t, []*schema.ToolResult{rendered})[0]
	require.Equal(t, background.StatusWaitingInput, response.Status)
	require.NotNil(t, response.InputRequest)
	require.Equal(t, "approval", response.InputRequest.ID)
	require.JSONEq(t, `{"question":"Approve?"}`, string(response.InputRequest.Data))

	_, err = manager.SendInput(context.Background(), &taskcore.SendInputRequest{
		TaskID: task.Spec.ID,
		Input:  taskcore.Input{EventID: "noise", Kind: "notification"},
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	stillWaiting, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusWaitingInput, stillWaiting.Status)

	_, err = manager.SendInput(context.Background(), &taskcore.SendInputRequest{
		TaskID: task.Spec.ID,
		Input: taskcore.Input{
			EventID: "resume-1", Kind: ResumeInputKind, Data: []byte("reject"),
		},
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	stillWaiting, err = manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusWaitingInput, stillWaiting.Status)
	require.Empty(t, resumeRequests)
	input, err = ReadInputRequest(stillWaiting)
	require.NoError(t, err)
	require.Equal(t, "approval", input.ID)

	_, err = manager.SendInput(context.Background(), &taskcore.SendInputRequest{
		TaskID: task.Spec.ID,
		Input: taskcore.Input{
			EventID: "resume-2", Kind: ResumeInputKind, Data: []byte("approve"),
		},
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err = manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	input, err = ReadInputRequest(waiting)
	require.NoError(t, err)
	require.Equal(t, "region", input.ID)

	_, err = manager.SendInput(context.Background(), &taskcore.SendInputRequest{
		TaskID: task.Spec.ID,
		Input: taskcore.Input{
			EventID: "resume-3", Kind: ResumeInputKind, Data: []byte("us-east"),
		},
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	completed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, completed.Status)
	require.Equal(t, "done", string(completed.ResultData))
	require.Len(t, resumeRequests, 2)
	require.Equal(t, "approval", resumeRequests[0].RequestID)
	require.Equal(t, "approve", string(resumeRequests[0].Data))
	require.JSONEq(
		t,
		`{"run_id":"input-run","stage":"approval"}`,
		string(resumeRequests[0].Checkpoint),
	)
	require.Equal(t, "region", resumeRequests[1].RequestID)
	require.Equal(t, "us-east", string(resumeRequests[1].Data))
	require.JSONEq(
		t,
		`{"run_id":"input-run","stage":"region"}`,
		string(resumeRequests[1].Checkpoint),
	)
}

func TestManagedToolReplaysResumeAfterWorkerHandoff_BitsUT(t *testing.T) {
	resumeStarted := make(chan struct{})
	var startOnce sync.Once
	var mu sync.Mutex
	var resumeRequests []*ResumeRequest
	implementation := &resumableFakeTool{
		fakeTool: &fakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{wait: func(context.Context) (*Outcome, error) {
					return &Outcome{
						Status: taskcore.OutcomeInterrupted,
						InputRequest: &InputRequest{
							ID: "approval", Data: []byte(`{"question":"Approve?"}`),
						},
					}, nil
				}}, nil
			},
			recover: func(context.Context, *RecoverRequest) (Run, error) {
				return &fakeRun{wait: func(context.Context) (*Outcome, error) {
					return &Outcome{
						Status: taskcore.OutcomeCompleted, Data: []byte("approved"),
					}, nil
				}}, nil
			},
		},
		resume: func(_ context.Context, request *ResumeRequest) (Run, error) {
			mu.Lock()
			copy := *request
			copy.Data = append([]byte(nil), request.Data...)
			resumeRequests = append(resumeRequests, &copy)
			call := len(resumeRequests)
			mu.Unlock()
			if call == 1 {
				return &fakeRun{wait: func(ctx context.Context) (*Outcome, error) {
					startOnce.Do(func() { close(resumeStarted) })
					<-ctx.Done()
					return nil, ctx.Err()
				}}, nil
			}
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{
					Status: taskcore.OutcomeCompleted, Data: []byte("approved"),
				}, nil
			}}, nil
		},
	}
	store := background.NewInMemoryStore(nil)
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	managerOne := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store})
	require.NoError(t, registerExecutors(managerOne, registry))
	task, err := managerOne.Submit(context.Background(), &background.SubmitRequest{
		Spec: background.Spec{
			ID: "resume-handoff", ExecutorKey: RecoverableExecutorKey,
			Kind:    "background_tool",
			Payload: encodedPayload(t, "external", `{"value":"work"}`),
		},
	})
	require.NoError(t, err)
	require.NoError(t, managerOne.Execute(context.Background(), task.Spec.ID))
	_, err = managerOne.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	_, err = managerOne.SendInput(context.Background(), &taskcore.SendInputRequest{
		TaskID: task.Spec.ID,
		Input: taskcore.Input{
			EventID: "resume", Kind: ResumeInputKind, Data: []byte("yes"),
		},
	})
	require.NoError(t, err)

	executeDone := make(chan error, 1)
	go func() {
		executeDone <- managerOne.Execute(context.Background(), task.Spec.ID)
	}()
	<-resumeStarted
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	require.NoError(t, managerOne.Close(closeCtx))
	cancel()
	require.NoError(t, <-executeDone)
	yielded, err := managerOne.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, yielded.Status)

	managerTwo := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store})
	require.NoError(t, registerExecutors(managerTwo, registry))
	require.NoError(t, managerTwo.Execute(context.Background(), task.Spec.ID))
	completed, err := managerTwo.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusCompleted, completed.Status)
	require.Equal(t, "approved", string(completed.ResultData))
	require.Equal(t, int64(3), completed.Attempt)
	require.Len(t, resumeRequests, 1)
}

func TestManagedToolAutoBackgroundAndStop(t *testing.T) {
	stopped := make(chan struct{})
	var stopOnce sync.Once
	implementation := &autoBackgroundFakeTool{fakeTool: &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(ctx context.Context) (*Outcome, error) {
					select {
					case <-stopped:
						return &Outcome{Status: taskcore.OutcomeCanceled}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
				stop: func(context.Context) error {
					stopOnce.Do(func() { close(stopped) })
					return nil
				},
			}, nil
		},
	}}
	manager, wrapped := newTestManagedTool(t, implementation, 5*time.Millisecond)
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"slow"}`),
	)
	require.NoError(t, err)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, background.StatusRunning, event.Status)
	task := waitTaskAttempt(t, manager, event.TaskID)
	require.Equal(t, int64(1), task.Attempt)
	require.Equal(t, background.PublicationOnBackground, task.Publication)

	stoppedTask, err := manager.RequestCancel(context.Background(), event.TaskID)
	require.NoError(t, err)
	require.Equal(t, background.StatusRunning, stoppedTask.Status)
	deadline := time.Now().Add(time.Second)
	for stoppedTask.Status != background.StatusCanceled && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		stoppedTask, err = manager.Get(context.Background(), event.TaskID)
		require.NoError(t, err)
	}
	require.Equal(t, background.StatusCanceled, stoppedTask.Status)
}

func TestManagedToolPlainToolCanAutoBackgroundTaskFirst(t *testing.T) {
	started := make(chan *StartRequest, 1)
	release := make(chan struct{})
	implementation := &plainFakeTool{
		start: func(_ context.Context, request *StartRequest) (Run, error) {
			copy := *request
			started <- &copy
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				<-release
				return &Outcome{
					Status: taskcore.OutcomeCompleted,
					Data:   []byte("done"),
				}, nil
			}}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("plain"), Tool: implementation,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "plain-task", nil
		},
	})
	timeoutMs := 1
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "plain",
		ForegroundTimeoutMs: &timeoutMs,
		ShouldAutoBackground: func(
			context.Context,
			*foreground.CandidateInfo,
		) bool {
			return true
		},
	})
	require.NoError(t, err)
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(),
		toolArgument(`{"value":"slow"}`),
	)
	require.NoError(t, err)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, ManagedToolResponseEventLaunchResult, event.Type)
	require.Equal(t, "plain-task", event.TaskID)
	request := <-started
	require.Equal(t, int64(1), request.Attempt)
	close(release)
	completed := waitTaskTerminal(t, manager, event.TaskID)
	require.Equal(t, background.StatusCompleted, completed.Status)
	require.Equal(t, background.PublicationOnBackground, completed.Publication)
}

func TestManagedToolTaskFirstCallerAbortPolicyWithoutAutoBackground(t *testing.T) {
	newTool := func(
		t *testing.T,
		cancelOnAbort bool,
	) (*background.Manager, componenttool.BaseTool, <-chan struct{}, chan<- struct{}, *int64) {
		t.Helper()
		started := make(chan struct{})
		release := make(chan struct{})
		var stopCalls int64
		var stopOnce sync.Once
		implementation := &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				close(started)
				return &fakeRun{
					wait: func(ctx context.Context) (*Outcome, error) {
						select {
						case <-release:
							return &Outcome{
								Status: taskcore.OutcomeCompleted,
								Data:   []byte("done"),
							}, nil
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					},
					stop: func(context.Context) error {
						atomic.AddInt64(&stopCalls, 1)
						stopOnce.Do(func() { close(release) })
						return nil
					},
				}, nil
			},
		}
		registry := NewRegistry()
		require.NoError(t, registry.Register(&Registration{
			Info: toolInfo("external"), Tool: implementation,
		}))
		manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
			IDGen: func(
				context.Context,
				*background.AllocateTaskIDRequest,
			) (string, error) {
				return "task-fixed", nil
			},
		})
		timeoutMs := 0
		wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
			Manager: manager, Registry: registry, ToolName: "external",
			ForegroundTimeoutMs: &timeoutMs,
			ShouldCancelOnCallerAbort: func(
				context.Context,
				*foreground.CallerAbortInfo,
			) bool {
				return cancelOnAbort
			},
			SessionID: func(context.Context) (string, error) {
				return "session", nil
			},
		})
		require.NoError(t, err)
		return manager, wrapped, started, release, &stopCalls
	}

	t.Run("default detaches", func(t *testing.T) {
		manager, wrapped, started, release, stopCalls := newTool(t, false)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan struct {
			value *schema.ToolResult
			err   error
		}, 1)
		go func() {
			value, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
				ctx,
				toolArgument(`{"value":"slow"}`),
			)
			result <- struct {
				value *schema.ToolResult
				err   error
			}{value: value, err: err}
		}()
		<-started
		cancel()
		returned := <-result
		require.NoError(t, returned.err)
		event := decodeEvents(t, []*schema.ToolResult{returned.value})[0]
		require.Equal(t, ManagedToolResponseEventLaunchResult, event.Type)
		require.Equal(t, background.StatusRunning, event.Status)
		require.Zero(t, atomic.LoadInt64(stopCalls))
		close(release)
		require.Equal(
			t,
			background.StatusCompleted,
			waitTaskTerminal(t, manager, event.TaskID).Status,
		)
	})

	t.Run("explicit policy cancels", func(t *testing.T) {
		manager, wrapped, started, _, stopCalls := newTool(t, true)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan struct {
			value *schema.ToolResult
			err   error
		}, 1)
		go func() {
			value, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
				ctx,
				toolArgument(`{"value":"slow"}`),
			)
			result <- struct {
				value *schema.ToolResult
				err   error
			}{value: value, err: err}
		}()
		<-started
		cancel()
		returned := <-result
		require.NoError(t, returned.err)
		event := decodeEvents(t, []*schema.ToolResult{returned.value})[0]
		require.Equal(t, ManagedToolResponseEventForegroundResult, event.Type)
		require.Equal(t, background.StatusCanceled, event.Status)
		require.Equal(t, int64(1), atomic.LoadInt64(stopCalls))
		task := waitTaskTerminal(t, manager, "task-fixed")
		require.Equal(t, background.StatusCanceled, task.Status)
	})
}

func TestManagedToolStreamTaskFirstCallerAbortPolicyWithoutAutoBackground(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		cancelOnAbort bool
	}{
		{name: "detach", cancelOnAbort: false},
		{name: "cancel", cancelOnAbort: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			var stopCalls int64
			implementation := &plainFakeTool{
				start: func(context.Context, *StartRequest) (Run, error) {
					close(started)
					return &fakeRun{
						wait: func(ctx context.Context) (*Outcome, error) {
							select {
							case <-release:
								return &Outcome{
									Status: taskcore.OutcomeCompleted,
									Data:   []byte("done"),
								}, nil
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						},
						stop: func(context.Context) error {
							atomic.AddInt64(&stopCalls, 1)
							releaseOnce.Do(func() { close(release) })
							return nil
						},
					}, nil
				},
			}
			registry := NewRegistry()
			require.NoError(t, registry.Register(&Registration{
				Info: toolInfo("external"), Tool: implementation,
			}))
			manager := mustNewBackgroundManager(
				t,
				context.Background(),
				&background.Config{
					IDGen: func(
						context.Context,
						*background.AllocateTaskIDRequest,
					) (string, error) {
						return "stream-task", nil
					},
				},
			)
			timeoutMs := 0
			wrapped, err := NewManagedTool(
				context.Background(),
				&ManagedToolConfig{
					Manager: manager, Registry: registry, ToolName: "external",
					ForegroundTimeoutMs: &timeoutMs,
					ShouldCancelOnCallerAbort: func(
						context.Context,
						*foreground.CallerAbortInfo,
					) bool {
						return testCase.cancelOnAbort
					},
					SessionID: func(context.Context) (string, error) {
						return "session", nil
					},
				},
			)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
				ctx,
				toolArgument(`{"value":"slow"}`),
			)
			require.NoError(t, err)
			<-started
			cancel()
			_, recvErr := stream.Recv()
			require.ErrorIs(t, recvErr, io.EOF)

			snapshot, err := manager.Get(context.Background(), "stream-task")
			require.NoError(t, err)
			if testCase.cancelOnAbort {
				snapshot = waitTaskTerminal(t, manager, snapshot.Spec.ID)
				require.Equal(t, background.StatusCanceled, snapshot.Status)
				require.Equal(t, int64(1), atomic.LoadInt64(&stopCalls))
				return
			}
			require.Equal(t, background.PublicationOnBackground, snapshot.Publication)
			require.Zero(t, atomic.LoadInt64(&stopCalls))
			releaseOnce.Do(func() { close(release) })
			snapshot = waitTaskTerminal(t, manager, snapshot.Spec.ID)
			require.Equal(t, background.StatusCompleted, snapshot.Status)
		})
	}
}

func TestManagedToolStreamPersistsBeforeNDJSONProjection(t *testing.T) {
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			reader, writer := schema.Pipe[*Update](3)
			updateSent := make(chan struct{})
			go func() {
				for _, eventID := range []string{"event-1", "event-2", "event-3"} {
					writer.Send(&Update{
						EventID: eventID, Kind: "stdout", Data: []byte(eventID),
					}, nil)
				}
				writer.Close()
				close(updateSent)
			}()
			return &updatingRun{fakeRun: &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					<-updateSent
					return &Outcome{
						Status: taskcore.OutcomeCompleted, Data: []byte("done"),
					}, nil
				},
			},
				updates: reader,
			}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"stream"}`),
	)
	require.NoError(t, err)
	records := readAllStreamResults(t, stream)
	events := decodeEvents(t, records)
	require.Len(t, events, 4)
	for _, event := range events[:3] {
		require.Equal(t, ManagedToolResponseEventUpdate, event.Type)
	}
	require.Equal(t, ManagedToolResponseEventForegroundResult, events[3].Type)
	require.Empty(t, events[3].TaskID)

	_, err = manager.Get(context.Background(), "task-fixed")
	require.ErrorIs(t, err, background.ErrNotFound)
}

func TestManagedToolDrainYieldsAndRecoversWithoutStop(t *testing.T) {
	store := background.NewInMemoryStore(nil)
	registry := NewRegistry()
	started := make(chan struct{})
	var startedOnce sync.Once
	recovered := make(chan *RecoverRequest, 1)
	toolCheckpoint := []byte(`{"run_id":"business-run"}`)
	var stopCalls int
	var mu sync.Mutex
	implementation := &autoBackgroundFakeTool{fakeTool: &fakeTool{
		startCheckpoint: toolCheckpoint,
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(ctx context.Context) (*Outcome, error) {
					startedOnce.Do(func() { close(started) })
					<-ctx.Done()
					return nil, ctx.Err()
				},
				stop: func(context.Context) error {
					mu.Lock()
					stopCalls++
					mu.Unlock()
					return nil
				},
			}, nil
		},
		recover: func(_ context.Context, request *RecoverRequest) (Run, error) {
			recovered <- request
			return &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					return &Outcome{Status: taskcore.OutcomeCompleted, Data: []byte("done")}, nil
				},
			}, nil
		},
	}}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	managerOne := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store, IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "recover-task", nil
		},
	})
	timeout := time.Millisecond
	timeoutMs := int(timeout / time.Millisecond)
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: managerOne, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs:  &timeoutMs,
		ShouldAutoBackground: func(context.Context, *foreground.CandidateInfo) bool { return true },
		SessionID:            func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	_, err = wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"recover"}`),
	)
	require.NoError(t, err)
	<-started
	waitTaskAttempt(t, managerOne, "recover-task")
	toolCheckpoint[0] = 'X'
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	require.NoError(t, managerOne.Close(closeCtx))
	cancel()
	yielded, err := managerOne.Get(context.Background(), "recover-task")
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, yielded.Status)
	require.NotEmpty(t, yielded.Checkpoint)

	managerTwo := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store})
	require.NoError(t, registerExecutors(managerTwo, registry))
	require.NoError(t, managerTwo.Execute(context.Background(), "recover-task"))
	request := <-recovered
	require.Equal(t, "recover-task", request.TaskID)
	require.Equal(t, int64(2), request.Attempt)
	require.JSONEq(
		t,
		`{"run_id":"business-run"}`,
		string(request.Checkpoint),
	)
	mu.Lock()
	require.Zero(t, stopCalls)
	mu.Unlock()
}

func TestRecoverWithoutCheckpointUsesPersistedStartedGate(t *testing.T) {
	recovered := false
	implementation := &autoBackgroundFakeTool{fakeTool: &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return nil, errors.New("started operation must not restart")
		},
		recover: func(_ context.Context, request *RecoverRequest) (Run, error) {
			recovered = true
			require.Nil(t, request.Checkpoint)
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	checkpoint, err := encodeManagedCheckpoint(nil, nil)
	require.NoError(t, err)
	result, err := (&executor{registry: registry, recoverable: true}).Execute(
		context.Background(),
		&background.TaskSnapshot{
			Spec: background.Spec{
				ID: "recover-task", ExecutorKey: RecoverableExecutorKey,
				Kind:    "background_tool",
				Payload: encodedPayload(t, "external", `{"value":"recover"}`),
			},
			Status: background.StatusRunning, Attempt: 2,
			Checkpoint: checkpoint,
		},
		&replayRuntimeStub{},
	)
	require.NoError(t, err)
	require.True(t, recovered)
	require.Equal(t, background.ExecutionActionComplete, result.Action)
}

func TestManagedToolMaterializerIsDerivedAndFailureIsNonTerminal(t *testing.T) {
	materializer := &materializerStub{err: errors.New("derived file unavailable")}
	registry := NewRegistry()
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			reader, writer := schema.Pipe[*Update](1)
			sent := make(chan struct{})
			go func() {
				writer.Send(&Update{
					EventID: "line-1", Kind: "stdout", Data: []byte("hello"),
				}, nil)
				writer.Close()
				close(sent)
			}()
			return &updatingRun{
				fakeRun: &fakeRun{wait: func(context.Context) (*Outcome, error) {
					<-sent
					time.Sleep(time.Millisecond)
					return &Outcome{Status: taskcore.OutcomeCompleted}, nil
				}},
				updates: reader,
			}, nil
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation, Materializer: materializer,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "materialized", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		RunInBackground: func(context.Context, string) bool { return true },
		SessionID:       func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	_, err = wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"x"}`),
	)
	require.NoError(t, err)
	task := waitTaskAttempt(t, manager, "materialized")
	deadline := time.Now().Add(time.Second)
	for task.Status == background.StatusPending || task.Status == background.StatusRunning {
		require.True(t, time.Now().Before(deadline), "task did not finish")
		time.Sleep(time.Millisecond)
		task, err = manager.Get(context.Background(), "materialized")
		require.NoError(t, err)
	}
	require.Equal(t, background.StatusCompleted, task.Status)
	require.Equal(t, "/outputs/materialized", task.Spec.OutputFile)
	require.Contains(t, task.OutputFileErr, "derived file unavailable")
	output, err := manager.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: task.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, output.Parts, 1)
	materializer.mu.Lock()
	require.Len(t, materializer.requests, 1)
	require.Equal(t, "line-1", materializer.requests[0].EventID)
	materializer.mu.Unlock()
}

func TestManagedToolPlainRegistrationUsesFailExecutor(t *testing.T) {
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	manager, wrapped := newTestManagedTool(t, implementation, time.Second)
	wrapped.(*managedTool).runInBackground = func(context.Context, string) bool { return true }
	_, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"plain"}`),
	)
	require.NoError(t, err)
	task := waitTaskAttempt(t, manager, "task-fixed")
	require.Equal(t, ExecutorKey, task.Spec.ExecutorKey)
	require.Equal(t, background.LeaseExpiryFail, task.LeaseExpiryPolicy)
}

func TestAttack_PlainUpdateGeneratedEventIDNotMaterialized(t *testing.T) {
	materializer := &materializerStub{}
	registry := NewRegistry()
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return updatingRunFrom([]*Update{{
				Kind: "stdout", Data: []byte("plain"),
			}}, true), nil
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("plain"), Tool: implementation, Materializer: materializer,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "plain-generated-event", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "plain",
		ForegroundTimeoutMs: func() *int { value := 1000; return &value }(),
		ShouldAutoBackground: func(
			context.Context,
			*foreground.CandidateInfo,
		) bool {
			return true
		},
		SessionID: func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"plain"}`),
	)
	require.NoError(t, err)
	projected := decodeEvents(t, readAllStreamResults(t, stream))
	require.Len(t, projected, 2)
	require.Equal(t, ManagedToolResponseEventForegroundResult, projected[len(projected)-1].Type)
	projectedUpdateIDs := make(map[string]struct{})
	for _, event := range projected[:len(projected)-1] {
		require.Equal(t, ManagedToolResponseEventUpdate, event.Type)
		require.NotNil(t, event.Update)
		require.NotEmpty(t, event.Update.EventID)
		projectedUpdateIDs[event.Update.EventID] = struct{}{}
	}
	require.Len(t, projectedUpdateIDs, 1)
	task := waitTaskTerminal(t, manager, "plain-generated-event")
	require.Equal(t, background.StatusCompleted, task.Status)

	result, err := manager.ListTaskEvents(
		context.Background(),
		&background.ListTaskEventsRequest{TaskID: "plain-generated-event"},
	)
	require.NoError(t, err)
	require.Len(t, result.Parts, 1)
	require.NotNil(t, result.Parts[0])
	require.NotEmpty(t, result.Parts[0].EventID)
	_, ok := projectedUpdateIDs[result.Parts[0].EventID]
	require.True(t, ok)
	materializer.mu.Lock()
	require.Empty(t, materializer.requests)
	materializer.mu.Unlock()
}

func TestManagedToolRejectsNilRichResultWithoutPartialResult(t *testing.T) {
	registry := NewRegistry()
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: taskcore.OutcomeCompleted}, nil
			}}, nil
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
		RenderResult: func(context.Context, *background.TaskSnapshot) (*schema.ToolResult, error) {
			return nil, nil
		},
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "invalid-output", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		SessionID: func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"x"}`),
	)
	require.ErrorContains(t, err, "result renderer returned nil")
	require.Nil(t, result)
}

func TestManagedToolReturnsControlEnvelopeAndRichResult_BitsUT(t *testing.T) {
	imageURL := "https://example.com/result.png"
	registry := NewRegistry()
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{
					Status: taskcore.OutcomeCompleted,
					Data:   []byte(`{"internal":"result"}`),
				}, nil
			}}, nil
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
		RenderResult: func(
			_ context.Context,
			task *background.TaskSnapshot,
		) (*schema.ToolResult, error) {
			require.JSONEq(t, `{"internal":"result"}`, string(task.ResultData))
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{
				{Type: schema.ToolPartTypeText, Text: "render complete"},
				{
					Type: schema.ToolPartTypeImage,
					Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{
						URL: &imageURL,
					}},
				},
			}}, nil
		},
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "rich-output", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		SessionID: func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)

	_, isStandardInvoke := wrapped.(componenttool.InvokableTool)
	_, isStandardStream := wrapped.(componenttool.StreamableTool)
	require.False(t, isStandardInvoke)
	require.False(t, isStandardStream)
	require.Implements(t, (*componenttool.EnhancedInvokableTool)(nil), wrapped)
	require.Implements(t, (*componenttool.EnhancedStreamableTool)(nil), wrapped)

	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"x"}`),
	)
	require.NoError(t, err)
	require.Len(t, result.Parts, 3)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Equal(t, ManagedToolResponseEventForegroundResult, event.Type)
	require.Empty(t, event.TaskID)
	require.Equal(t, background.StatusCompleted, event.Status)
	require.Nil(t, event.Output)
	require.Equal(t, schema.ToolPartTypeText, result.Parts[1].Type)
	require.Equal(t, "render complete", result.Parts[1].Text)
	require.Equal(t, schema.ToolPartTypeImage, result.Parts[2].Type)
	require.Equal(t, imageURL, *result.Parts[2].Image.URL)
}

func TestManagedToolProjectionDetachesWhilePersistenceContinues(t *testing.T) {
	finished := make(chan struct{})
	implementation := &autoBackgroundFakeTool{fakeTool: &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			reader, writer := schema.Pipe[*Update](1)
			go func() {
				time.Sleep(20 * time.Millisecond)
				writer.Send(&Update{
					EventID: "late", Kind: "stdout", Data: []byte("late output"),
				}, nil)
				writer.Close()
				close(finished)
			}()
			return &updatingRun{
				fakeRun: &fakeRun{wait: func(context.Context) (*Outcome, error) {
					<-finished
					return &Outcome{Status: taskcore.OutcomeCompleted}, nil
				}},
				updates: reader,
			}, nil
		},
	}}
	manager, wrapped := newTestManagedTool(t, implementation, 5*time.Millisecond)
	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"detach"}`),
	)
	require.NoError(t, err)
	records := readAllStreamResults(t, stream)
	events := decodeEvents(t, records)
	require.Len(t, events, 1)
	require.Equal(t, ManagedToolResponseEventLaunchResult, events[0].Type)
	require.Equal(t, background.StatusRunning, events[0].Status)
	waitTaskAttempt(t, manager, "task-fixed")

	task := waitTaskTerminal(t, manager, "task-fixed")
	require.Equal(t, background.StatusCompleted, task.Status)
	output, err := manager.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: "task-fixed",
	})
	require.NoError(t, err)
	require.Len(t, output.Parts, 1)
	require.Equal(t, "late", output.Parts[0].EventID)
}

func TestAttack_LeaseLossBeforeStartedCheckpointRetriesStart(t *testing.T) {
	store := background.NewInMemoryStore(&background.InMemoryStoreConfig{
		ActiveAttemptTimeout: 5 * time.Millisecond,
	})
	registry := NewRegistry()
	stopCalled := make(chan struct{}, 1)
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(ctx context.Context) (*Outcome, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
				stop: func(context.Context) error {
					stopCalled <- struct{}{}
					return nil
				},
			}, nil
		},
		recover: func(context.Context, *RecoverRequest) (Run, error) {
			return nil, errors.New("recovery without a started checkpoint")
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, ToolName: "external", Arguments: `{"value":"x"}`,
	})
	require.NoError(t, err)
	created, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: background.Spec{
			ID: "lost", ExecutorKey: RecoverableExecutorKey,
			Kind: "background_tool", Payload: payload,
		},
		LeaseExpiryPolicy: background.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	_, err = store.Start(context.Background(), &background.StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store})
	require.NoError(t, registerExecutors(manager, registry))
	pending, err := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, pending.Status)
	requested, err := manager.RequestCancel(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, requested.Status)
	require.NotNil(t, requested.CancelRequestedAt)

	require.NoError(t, manager.Execute(context.Background(), created.Spec.ID))
	select {
	case <-stopCalled:
	case <-time.After(time.Second):
		t.Fatal("recovered logical operation was not stopped")
	}
	canceled, err := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusCanceled, canceled.Status)
	require.Equal(t, int64(2), canceled.Attempt)
	t.Log("missing started checkpoint selected idempotent Start instead of Recover")
}
