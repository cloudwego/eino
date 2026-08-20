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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/foreground"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
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

type handoffFakeTool struct {
	*fakeTool
}

func (*handoffFakeTool) Adopt(_ context.Context, req *AdoptRequest) (*AdoptResult, error) {
	return &AdoptResult{
		Run:            req.Run,
		ToolCheckpoint: append([]byte(nil), req.ToolCheckpoint...),
	}, nil
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
	implementation BackgroundTool,
	timeout time.Duration,
) (*backgroundtask.Manager, componenttool.BaseTool) {
	t.Helper()
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
		Description: func(string) string { return "External operation" },
	}))
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	})
	timeoutMs := int(timeout / time.Millisecond)
	config := &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
		SessionID:           func(context.Context) (string, error) { return "session", nil },
	}
	if _, ok := implementation.(ForegroundHandoffTool); ok {
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

func waitTaskAttempt(t *testing.T, manager *backgroundtask.Manager, taskID string) *backgroundtask.Task {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		task, err := manager.Get(context.Background(), taskID)
		require.NoError(t, err)
		if task.Attempt > 0 || task.Status != backgroundtask.StatusPending {
			return task
		}
		require.True(t, time.Now().Before(deadline), "task was not claimed")
		time.Sleep(time.Millisecond)
	}
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
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)

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
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)
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
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)
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
			return &Outcome{Status: backgroundtask.StatusCompleted}, nil
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

func TestManagedToolFastCompletionReturnsCanonicalTaskID(t *testing.T) {
	implementation := &fakeTool{
		start: func(_ context.Context, request *StartRequest) (Run, error) {
			require.Equal(t, "task-fixed", request.TaskID)
			return &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					return &Outcome{
						Status: backgroundtask.StatusCompleted,
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
	require.Equal(t, backgroundtask.StatusCompleted, events[0].Status)
	require.Equal(t, map[string]any{"answer": float64(42)}, events[0].Output)

	_, err = manager.Get(context.Background(), "task-fixed")
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)
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
							Status: backgroundtask.StatusWaitingInput,
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
					Status: backgroundtask.StatusCompleted,
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
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)

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
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)

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
							Status: backgroundtask.StatusWaitingInput,
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
						Status: backgroundtask.StatusWaitingInput,
						InputRequest: &InputRequest{
							ID: "region", Data: []byte(`{"question":"Which region?"}`),
						},
						Checkpoint: []byte(
							`{"run_id":"input-run","stage":"region"}`,
						),
					}, nil
				}
				return &Outcome{
					Status: backgroundtask.StatusCompleted, Data: []byte("done"),
				}, nil
			}}, nil
		},
	}
	store := backgroundtask.NewInMemoryStore(nil)
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, RegisterExecutors(executors, registry))
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: store, Executors: executors,
	})
	task, err := manager.Submit(context.Background(), &backgroundtask.SubmitRequest{
		Spec: backgroundtask.Spec{
			ID: "durable-input", ExecutorKey: RecoverableExecutorKey,
			Kind:    "background_tool",
			Payload: encodedPayload(t, "external", `{"value":"work"}`),
		},
	})
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, waiting.Status)
	input, err := ReadInputRequest(waiting)
	require.NoError(t, err)
	require.Equal(t, "approval", input.ID)
	require.JSONEq(t, `{"question":"Approve?"}`, string(input.Data))
	rendered, err := (&managedTool{registration: &Registration{}}).
		renderLaunchResult(context.Background(), waiting)
	require.NoError(t, err)
	require.Contains(t, rendered.Parts[0].Text, `"data":{"question":"Approve?"}`)
	response := decodeEvents(t, []*schema.ToolResult{rendered})[0]
	require.Equal(t, backgroundtask.StatusWaitingInput, response.Status)
	require.NotNil(t, response.InputRequest)
	require.Equal(t, "approval", response.InputRequest.ID)
	require.JSONEq(t, `{"question":"Approve?"}`, string(response.InputRequest.Data))

	pending, err := manager.Resume(context.Background(), &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version, Data: []byte("reject"),
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), pending.Spec.ID))
	stillWaiting, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, stillWaiting.Status)
	require.Empty(t, resumeRequests)
	input, err = ReadInputRequest(stillWaiting)
	require.NoError(t, err)
	require.Equal(t, "approval", input.ID)

	pending, err = manager.Resume(context.Background(), &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: stillWaiting.Version, Data: []byte("approve"),
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), pending.Spec.ID))
	waiting, err = manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	input, err = ReadInputRequest(waiting)
	require.NoError(t, err)
	require.Equal(t, "region", input.ID)

	pending, err = manager.Resume(context.Background(), &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version, Data: []byte("us-east"),
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), pending.Spec.ID))
	completed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCompleted, completed.Status)
	require.Equal(t, "done", string(completed.ResultData))
	require.Nil(t, completed.PendingResume)
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
						Status: backgroundtask.StatusWaitingInput,
						InputRequest: &InputRequest{
							ID: "approval", Data: []byte(`{"question":"Approve?"}`),
						},
					}, nil
				}}, nil
			},
			recover: func(context.Context, *RecoverRequest) (Run, error) {
				t.Fatal("waiting-input recovery must replay Resume")
				return nil, nil
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
					Status: backgroundtask.StatusCompleted, Data: []byte("approved"),
				}, nil
			}}, nil
		},
	}
	store := backgroundtask.NewInMemoryStore(nil)
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	executorsOne := backgroundtask.NewExecutorRegistry()
	require.NoError(t, RegisterExecutors(executorsOne, registry))
	managerOne := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: store, Executors: executorsOne,
	})
	task, err := managerOne.Submit(context.Background(), &backgroundtask.SubmitRequest{
		Spec: backgroundtask.Spec{
			ID: "resume-handoff", ExecutorKey: RecoverableExecutorKey,
			Kind:    "background_tool",
			Payload: encodedPayload(t, "external", `{"value":"work"}`),
		},
	})
	require.NoError(t, err)
	require.NoError(t, managerOne.Execute(context.Background(), task.Spec.ID))
	waiting, err := managerOne.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	pending, err := managerOne.Resume(context.Background(), &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version, Data: []byte("yes"),
	})
	require.NoError(t, err)

	executeDone := make(chan error, 1)
	go func() {
		executeDone <- managerOne.Execute(context.Background(), pending.Spec.ID)
	}()
	<-resumeStarted
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	require.NoError(t, managerOne.Close(closeCtx))
	cancel()
	require.NoError(t, <-executeDone)
	yielded, err := managerOne.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusPending, yielded.Status)
	require.Equal(t, []byte("yes"), yielded.PendingResume)

	executorsTwo := backgroundtask.NewExecutorRegistry()
	require.NoError(t, RegisterExecutors(executorsTwo, registry))
	managerTwo := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: store, Executors: executorsTwo,
	})
	require.NoError(t, managerTwo.Execute(context.Background(), task.Spec.ID))
	completed, err := managerTwo.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCompleted, completed.Status)
	require.Equal(t, "approved", string(completed.ResultData))
	require.Equal(t, int64(3), completed.Attempt)
	require.Len(t, resumeRequests, 2)
	require.Equal(t, resumeRequests[0].RequestID, resumeRequests[1].RequestID)
	require.Equal(t, resumeRequests[0].Data, resumeRequests[1].Data)
}

func TestManagedToolAutoBackgroundAndStop(t *testing.T) {
	stopped := make(chan struct{})
	var stopOnce sync.Once
	implementation := &handoffFakeTool{fakeTool: &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(ctx context.Context) (*Outcome, error) {
					select {
					case <-stopped:
						return &Outcome{Status: backgroundtask.StatusCanceled}, nil
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
	require.Equal(t, backgroundtask.StatusPending, event.Status)
	task := waitTaskAttempt(t, manager, event.TaskID)
	require.Equal(t, int64(1), task.Attempt)

	stoppedTask, err := manager.RequestCancel(context.Background(), event.TaskID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusRunning, stoppedTask.Status)
	deadline := time.Now().Add(time.Second)
	for stoppedTask.Status != backgroundtask.StatusCanceled && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		stoppedTask, err = manager.Get(context.Background(), event.TaskID)
		require.NoError(t, err)
	}
	require.Equal(t, backgroundtask.StatusCanceled, stoppedTask.Status)
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
						Status: backgroundtask.StatusCompleted, Data: []byte("done"),
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
	defer stream.Close()
	var records []*schema.ToolResult
	for {
		record, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		require.NoError(t, recvErr)
		records = append(records, record)
	}
	events := decodeEvents(t, records)
	require.Len(t, events, 4)
	for _, event := range events[:3] {
		require.Equal(t, ManagedToolResponseEventUpdate, event.Type)
	}
	require.Equal(t, ManagedToolResponseEventForegroundResult, events[3].Type)
	require.Empty(t, events[3].TaskID)

	_, err = manager.Get(context.Background(), "task-fixed")
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)
}

func TestManagedToolDrainYieldsAndRecoversWithoutStop(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(nil)
	registry := NewRegistry()
	started := make(chan struct{})
	var startedOnce sync.Once
	recovered := make(chan *RecoverRequest, 1)
	toolCheckpoint := []byte(`{"run_id":"business-run"}`)
	var stopCalls int
	var mu sync.Mutex
	implementation := &handoffFakeTool{fakeTool: &fakeTool{
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
					return &Outcome{Status: backgroundtask.StatusCompleted, Data: []byte("done")}, nil
				},
			}, nil
		},
	}}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
	}))
	executorsOne := backgroundtask.NewExecutorRegistry()
	managerOne := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: store, Executors: executorsOne,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "recover-task", nil
		},
	})
	timeout := time.Millisecond
	timeoutMs := int(timeout / time.Millisecond)
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: managerOne, Executors: executorsOne, Registry: registry, ToolName: "external",
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
	require.Equal(t, backgroundtask.StatusPending, yielded.Status)
	require.NotEmpty(t, yielded.Checkpoint)

	executorsTwo := backgroundtask.NewExecutorRegistry()
	managerTwo := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: store, Executors: executorsTwo,
	})
	require.NoError(t, RegisterExecutors(executorsTwo, registry))
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
	implementation := &handoffFakeTool{fakeTool: &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return nil, errors.New("started operation must not restart")
		},
		recover: func(_ context.Context, request *RecoverRequest) (Run, error) {
			recovered = true
			require.Nil(t, request.Checkpoint)
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: backgroundtask.StatusCompleted}, nil
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
		&backgroundtask.Task{
			Spec: backgroundtask.Spec{
				ID: "recover-task", ExecutorKey: RecoverableExecutorKey,
				Kind:    "background_tool",
				Payload: encodedPayload(t, "external", `{"value":"recover"}`),
			},
			Status: backgroundtask.StatusRunning, Attempt: 2,
			Checkpoint: checkpoint,
		},
		&replayRuntimeStub{},
	)
	require.NoError(t, err)
	require.True(t, recovered)
	require.Equal(t, backgroundtask.StatusCompleted, result.Status)
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
					return &Outcome{Status: backgroundtask.StatusCompleted}, nil
				}},
				updates: reader,
			}, nil
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation, Materializer: materializer,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "materialized", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "external",
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
	for task.Status == backgroundtask.StatusPending || task.Status == backgroundtask.StatusRunning {
		require.True(t, time.Now().Before(deadline), "task did not finish")
		time.Sleep(time.Millisecond)
		task, err = manager.Get(context.Background(), "materialized")
		require.NoError(t, err)
	}
	require.Equal(t, backgroundtask.StatusCompleted, task.Status)
	require.Equal(t, "/outputs/materialized", task.Spec.OutputFile)
	require.Contains(t, task.OutputFileErr, "derived file unavailable")
	output, err := manager.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: task.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, output.Events, 1)
	materializer.mu.Lock()
	require.Len(t, materializer.requests, 1)
	require.Equal(t, "line-1", materializer.requests[0].EventID)
	materializer.mu.Unlock()
}

func TestManagedToolPlainRegistrationUsesFailExecutor(t *testing.T) {
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: backgroundtask.StatusCompleted}, nil
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
	require.Equal(t, backgroundtask.LeaseExpiryFail, task.LeaseExpiryPolicy)
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
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "plain-generated-event", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "plain",
		RunInBackground: func(context.Context, string) bool { return true },
		SessionID:       func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"plain"}`),
	)
	require.NoError(t, err)
	projected := decodeEvents(t, readAllStreamResults(t, stream))
	require.Len(t, projected, 2)
	require.Equal(t, ManagedToolResponseEventUpdate, projected[0].Type)
	require.NotNil(t, projected[0].Update)
	require.NotEmpty(t, projected[0].Update.EventID)

	result, err := manager.ListTaskEvents(
		context.Background(),
		&backgroundtask.ListTaskEventsRequest{TaskID: "plain-generated-event"},
	)
	require.NoError(t, err)
	require.Len(t, result.Events, 1)
	require.NotNil(t, result.Events[0])
	require.NotEmpty(t, result.Events[0].EventID)
	require.Equal(t, result.Events[0].EventID, projected[0].Update.EventID)
	materializer.mu.Lock()
	require.Empty(t, materializer.requests)
	materializer.mu.Unlock()
}

func TestManagedToolRejectsNilRichResultWithoutPartialResult(t *testing.T) {
	registry := NewRegistry()
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return &Outcome{Status: backgroundtask.StatusCompleted}, nil
			}}, nil
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
		RenderResult: func(context.Context, *backgroundtask.Task) (*schema.ToolResult, error) {
			return nil, nil
		},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "invalid-output", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "external",
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
					Status: backgroundtask.StatusCompleted,
					Data:   []byte(`{"internal":"result"}`),
				}, nil
			}}, nil
		},
	}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
		RenderResult: func(
			_ context.Context,
			task *backgroundtask.Task,
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
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "rich-output", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "external",
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
	require.Equal(t, backgroundtask.StatusCompleted, event.Status)
	require.Nil(t, event.Output)
	require.Equal(t, schema.ToolPartTypeText, result.Parts[1].Type)
	require.Equal(t, "render complete", result.Parts[1].Text)
	require.Equal(t, schema.ToolPartTypeImage, result.Parts[2].Type)
	require.Equal(t, imageURL, *result.Parts[2].Image.URL)
}

func TestManagedToolProjectionDetachesWhilePersistenceContinues(t *testing.T) {
	finished := make(chan struct{})
	implementation := &handoffFakeTool{fakeTool: &fakeTool{
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
					return &Outcome{Status: backgroundtask.StatusCompleted}, nil
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
	defer stream.Close()
	var records []*schema.ToolResult
	for {
		record, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		require.NoError(t, recvErr)
		records = append(records, record)
	}
	events := decodeEvents(t, records)
	require.Len(t, events, 1)
	require.Equal(t, ManagedToolResponseEventLaunchResult, events[0].Type)
	require.Equal(t, backgroundtask.StatusPending, events[0].Status)
	waitTaskAttempt(t, manager, "task-fixed")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		task, getErr := manager.Get(context.Background(), "task-fixed")
		require.NoError(t, getErr)
		if task.Status == backgroundtask.StatusCompleted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	output, err := manager.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: "task-fixed",
	})
	require.NoError(t, err)
	require.Len(t, output.Events, 1)
	require.Equal(t, "late", output.Events[0].EventID)
}

func TestAttack_LeaseLossBeforeStartedCheckpointRetriesStart(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(&backgroundtask.InMemoryStoreConfig{
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
	created, err := store.Create(context.Background(), &backgroundtask.CreateTaskRequest{
		Spec: backgroundtask.Spec{
			ID: "lost", ExecutorKey: RecoverableExecutorKey,
			Kind: "background_tool", Payload: payload,
		},
		LeaseExpiryPolicy: backgroundtask.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	_, err = store.Start(context.Background(), &backgroundtask.StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: store, Executors: executors,
	})
	require.NoError(t, RegisterExecutors(executors, registry))
	pending, err := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusPending, pending.Status)
	requested, err := manager.RequestCancel(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusPending, requested.Status)
	require.NotNil(t, requested.CancelRequestedAt)

	require.NoError(t, manager.Execute(context.Background(), created.Spec.ID))
	select {
	case <-stopCalled:
	case <-time.After(time.Second):
		t.Fatal("recovered logical operation was not stopped")
	}
	canceled, err := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCanceled, canceled.Status)
	require.Equal(t, int64(2), canceled.Attempt)
	t.Log("missing started checkpoint selected idempotent Start instead of Recover")
}
