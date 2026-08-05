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
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type fakeTool struct {
	start   func(context.Context, *StartRequest) (Run, error)
	recover func(context.Context, *RecoverRequest) (Run, error)
}

type plainFakeTool struct {
	start func(context.Context, *StartRequest) (Run, error)
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
func (t *plainFakeTool) Start(ctx context.Context, request *StartRequest) (Run, error) {
	return t.start(ctx, request)
}

func (*fakeTool) ValidateArguments(arguments string) error {
	var value map[string]any
	return json.Unmarshal([]byte(arguments), &value)
}
func (t *fakeTool) Start(ctx context.Context, request *StartRequest) (Run, error) {
	return t.start(ctx, request)
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
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs:  &timeoutMs,
		ShouldAutoBackground: func(context.Context, *backgroundtask.Task) bool { return true },
		SessionID:            func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	return manager, wrapped
}

func decodeEvents(t *testing.T, records []string) []*ManagedToolResponseEvent {
	t.Helper()
	events := make([]*ManagedToolResponseEvent, 0, len(records))
	for _, record := range records {
		var event ManagedToolResponseEvent
		require.NoError(t, json.Unmarshal([]byte(record), &event))
		events = append(events, &event)
	}
	return events
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

	_, err := wrapped.(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"value":"original"}`,
	)
	require.ErrorIs(t, err, prepareErr)
	_, err = manager.Get(context.Background(), "task-fixed")
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)

	reader, err := wrapped.(componenttool.StreamableTool).StreamableRun(
		context.Background(), `{"value":"original"}`,
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
	require.True(t, ok)
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
	task, err := manager.Get(ctx, "task-fixed")
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCompleted, task.Status)
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
) (Run, error) {
	t.mu.Lock()
	t.starts++
	t.arguments = request.Arguments
	t.mu.Unlock()
	return &fakeRun{wait: func(context.Context) (*Outcome, error) {
		return &Outcome{Status: backgroundtask.StatusCompleted}, nil
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
	result, err := wrapped.(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"value":"x"}`,
	)
	require.NoError(t, err)
	events := decodeEvents(t, []string{result})
	require.Equal(t, ManagedToolResponseEventLaunchResult, events[0].Type)
	require.Equal(t, "task-fixed", events[0].TaskID)
	require.Equal(t, backgroundtask.StatusCompleted, events[0].Status)
	require.Equal(t, map[string]any{"answer": float64(42)}, events[0].Output)

	task, err := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCompleted, task.Status)
	require.Equal(t, RecoverableExecutorKey, task.Spec.ExecutorKey)
}

func TestManagedToolAutoBackgroundAndStop(t *testing.T) {
	stopped := make(chan struct{})
	var stopOnce sync.Once
	implementation := &fakeTool{
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
	}
	manager, wrapped := newTestManagedTool(t, implementation, 5*time.Millisecond)
	result, err := wrapped.(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"value":"slow"}`,
	)
	require.NoError(t, err)
	event := decodeEvents(t, []string{result})[0]
	require.Equal(t, backgroundtask.StatusRunning, event.Status)
	task, err := manager.Get(context.Background(), event.TaskID)
	require.NoError(t, err)
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
	stream, err := wrapped.(componenttool.StreamableTool).StreamableRun(
		context.Background(), `{"value":"stream"}`,
	)
	require.NoError(t, err)
	defer stream.Close()
	var records []string
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
	require.Equal(t, ManagedToolResponseEventLaunchResult, events[3].Type)
	require.Equal(t, "task-fixed", events[3].TaskID)

	output, err := manager.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: "task-fixed",
	})
	require.NoError(t, err)
	require.Len(t, output.Events, 3)
	require.Equal(t, "event-1", output.Events[0].EventID)
}

func TestManagedToolDrainYieldsAndRecoversWithoutStop(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(nil)
	registry := NewRegistry()
	started := make(chan struct{})
	recovered := make(chan *RecoverRequest, 1)
	var stopCalls int
	var mu sync.Mutex
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(ctx context.Context) (*Outcome, error) {
					close(started)
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
	}
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
		ShouldAutoBackground: func(context.Context, *backgroundtask.Task) bool { return true },
		SessionID:            func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	_, err = wrapped.(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"value":"recover"}`,
	)
	require.NoError(t, err)
	<-started
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	require.NoError(t, managerOne.Close(closeCtx))
	cancel()
	yielded, err := managerOne.Get(context.Background(), "recover-task")
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusPending, yielded.Status)
	require.Empty(t, yielded.Checkpoint)

	executorsTwo := backgroundtask.NewExecutorRegistry()
	managerTwo := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Tasks: store, Executors: executorsTwo,
	})
	require.NoError(t, RegisterExecutors(executorsTwo, registry))
	require.NoError(t, managerTwo.Execute(context.Background(), "recover-task"))
	request := <-recovered
	require.Equal(t, "recover-task", request.TaskID)
	require.Equal(t, int64(2), request.Attempt)
	mu.Lock()
	require.Zero(t, stopCalls)
	mu.Unlock()
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
		SessionID: func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	_, err = wrapped.(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"value":"x"}`,
	)
	require.NoError(t, err)
	task, err := manager.Get(context.Background(), "materialized")
	require.NoError(t, err)
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
	_, err := wrapped.(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"value":"plain"}`,
	)
	require.NoError(t, err)
	task, err := manager.Get(context.Background(), "task-fixed")
	require.NoError(t, err)
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
		SessionID: func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	stream, err := wrapped.(componenttool.StreamableTool).StreamableRun(
		context.Background(), `{"value":"plain"}`,
	)
	require.NoError(t, err)
	projected := decodeEvents(t, readAllStreamRecords(t, stream))
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

func TestManagedToolRejectsInvalidFinalOutputWithoutPartialResult(t *testing.T) {
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
		LaunchOutput: func(context.Context, *backgroundtask.Task) (any, error) {
			return func() {}, nil
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
	result, err := wrapped.(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"value":"x"}`,
	)
	require.ErrorContains(t, err, "encode stream event")
	require.Empty(t, result)
}

func TestManagedToolProjectionDetachesWhilePersistenceContinues(t *testing.T) {
	finished := make(chan struct{})
	implementation := &fakeTool{
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
	}
	manager, wrapped := newTestManagedTool(t, implementation, 5*time.Millisecond)
	stream, err := wrapped.(componenttool.StreamableTool).StreamableRun(
		context.Background(), `{"value":"detach"}`,
	)
	require.NoError(t, err)
	defer stream.Close()
	var records []string
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
	require.Equal(t, backgroundtask.StatusRunning, events[0].Status)

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

func TestRecoverableCancellationAfterLeaseLossReattachesToStop(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(&backgroundtask.InMemoryStoreConfig{
		ActiveAttemptTimeout: 5 * time.Millisecond,
	})
	registry := NewRegistry()
	stopCalled := make(chan struct{}, 1)
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return nil, errors.New("unexpected duplicate start")
		},
		recover: func(context.Context, *RecoverRequest) (Run, error) {
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
}
