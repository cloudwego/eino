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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type replayRuntimeStub struct {
	inserted bool
	parts    []*background.TaskEventPart
}

func (*replayRuntimeStub) Controls() <-chan background.ControlRequest {
	return make(chan background.ControlRequest)
}
func (r *replayRuntimeStub) NewTaskEventWriter(
	eventID string,
) (background.TaskEventScope, background.TaskEventWriter) {
	if eventID == "" {
		eventID = "generated"
	}
	scope := background.TaskEventScope{
		TaskID: "attack-task", Attempt: 1, EventID: eventID,
	}
	return scope, replayTaskEventWriter{
		scope: scope, inserted: r.inserted, runtime: r,
	}
}

type replayTaskEventWriter struct {
	scope    background.TaskEventScope
	inserted bool
	runtime  *replayRuntimeStub
}

func (w replayTaskEventWriter) Append(
	_ context.Context,
	part *background.TaskEventPart,
) (*background.AppendTaskEventResult, error) {
	copy := *part
	copy.Data = append([]byte(nil), part.Data...)
	w.runtime.parts = append(w.runtime.parts, &copy)
	return &background.AppendTaskEventResult{
		Event: &background.TaskEvent{
			TaskID: w.scope.TaskID, EventID: w.scope.EventID,
			PartID: copy.PartID, Data: copy.Data, Final: copy.Final,
		},
		Inserted: w.inserted,
	}, nil
}

type customUpdateEventPersister struct {
	event *Update
}

func (p *customUpdateEventPersister) Persist(
	ctx context.Context,
	_ background.TaskEventScope,
	input *background.TaskEventEnvelope[*Update, *Update],
	writer background.TaskEventWriter,
) ([]*background.AppendTaskEventResult, error) {
	p.event = cloneUpdate(input.Event)
	result, err := writer.Append(ctx, &background.TaskEventPart{
		PartID: "custom", Data: append([]byte("custom:"), input.Event.Data...),
		Final: true,
	})
	if err != nil {
		return nil, err
	}
	return []*background.AppendTaskEventResult{result}, nil
}
func (*replayRuntimeStub) ReportTranscriptFailure(context.Context, error) error { return nil }
func (*replayRuntimeStub) ListInputs(
	context.Context,
	int64,
	int,
) (*task.ListInputsResult, error) {
	return &task.ListInputsResult{}, nil
}
func (*replayRuntimeStub) WaitInputs(
	context.Context,
	int64,
) (*task.ListInputsResult, error) {
	return &task.ListInputsResult{}, nil
}
func (*replayRuntimeStub) AdvanceInputCursor(context.Context, int64, int64) error {
	return nil
}
func (*replayRuntimeStub) CommitInput(context.Context, int64, int64, []byte) error {
	return nil
}
func (*replayRuntimeStub) CommitStart(context.Context, []byte) error { return nil }

type commitFailingRuntime struct {
	*replayRuntimeStub
	err error
}

type startResultTool struct {
	result *StartResult
	err    error
}

func (*startResultTool) ValidateArguments(string) error { return nil }
func (t *startResultTool) Start(
	context.Context,
	*StartRequest,
) (*StartResult, error) {
	return t.result, t.err
}

func (r *commitFailingRuntime) CommitStart(context.Context, []byte) error {
	return r.err
}

func newAttackManagedTool(
	t *testing.T,
	implementation Tool,
	materializer OutputMaterializer,
) (*background.Manager, componenttool.BaseTool) {
	t.Helper()
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("attack"), Tool: implementation, Materializer: materializer,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "attack-task", nil
		},
	})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "attack",
		RunInBackground: func(context.Context, string) bool { return true },
		SessionID:       func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	return manager, wrapped
}

func updatingRunFrom(
	updates []*Update,
	closeStream bool,
) Run {
	reader, writer := schema.Pipe[*Update](len(updates))
	sent := make(chan struct{})
	go func() {
		for _, update := range updates {
			writer.Send(update, nil)
		}
		if closeStream {
			writer.Close()
		}
		close(sent)
	}()
	return &updatingRun{
		fakeRun: &fakeRun{wait: func(context.Context) (*Outcome, error) {
			<-sent
			return &Outcome{Status: task.OutcomeCompleted}, nil
		}},
		updates: reader,
	}
}

func readAllStreamResults(
	t *testing.T,
	stream *schema.StreamReader[*schema.ToolResult],
) []*schema.ToolResult {
	t.Helper()
	defer stream.Close()
	var results []*schema.ToolResult
	for {
		result, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return results
		}
		require.NoError(t, err)
		results = append(results, result)
	}
}

func waitAttackTask(t *testing.T, manager *background.Manager) *background.TaskSnapshot {
	t.Helper()
	deadline := time.Now().Add(terminalUpdateDrainTime + time.Second)
	for {
		task, err := manager.Get(context.Background(), "attack-task")
		require.NoError(t, err)
		if task.Status != background.StatusPending &&
			task.Status != background.StatusRunning {
			return task
		}
		require.True(t, time.Now().Before(deadline), "task did not finish")
		time.Sleep(time.Millisecond)
	}
}

func TestAttack_ForegroundTimeoutUsesOnePolicySnapshot(t *testing.T) {
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(ctx context.Context) (*Outcome, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("snapshot"), Tool: implementation,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "snapshot-task", nil
		},
	})
	var calls int32
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "snapshot",
		InvocationTimeoutMs: func(context.Context, string) *int {
			call := atomic.AddInt32(&calls, 1)
			timeoutMs := 5
			if call > 1 {
				timeoutMs = 5_000
			}
			return &timeoutMs
		},
	})
	require.NoError(t, err)

	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{}`),
	)
	require.Nil(t, result)
	var timeoutErr *task.ForegroundTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, 5*time.Millisecond, timeoutErr.Timeout)
	require.Equal(t, "snapshot-task", timeoutErr.TaskID)
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))
	t.Log("the timer and structured error use the same timeout policy result")
}

func TestAttack_ForegroundTimeoutDoesNotMaskCallerDeadline(t *testing.T) {
	release := make(chan struct{})
	implementation := &plainFakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					<-release
					return &Outcome{Status: task.OutcomeCompleted}, nil
				},
			}, nil
		},
	}
	_, wrapped := newTestManagedTool(t, implementation, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		ctx, toolArgument(`{}`),
	)
	close(release)

	require.Nil(t, result)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var timeoutErr *task.ForegroundTimeoutError
	require.False(t, errors.As(err, &timeoutErr))
	t.Log("caller deadline retained its original error identity")
}

func TestAttack_ReplayedEventProjectsOnceMaterializesTwice(t *testing.T) {
	update := &Update{
		EventID: "stable", Kind: "stdout", Data: []byte("same"),
	}
	materializer := &materializerStub{}
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return updatingRunFrom([]*Update{update, cloneUpdate(update)}, true), nil
		},
	}
	manager, wrapped := newAttackManagedTool(t, implementation, materializer)
	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"replay"}`),
	)
	require.NoError(t, err)
	events := decodeEvents(t, readAllStreamResults(t, stream))
	require.NotEmpty(t, events)
	require.Equal(t, ManagedToolResponseEventLaunchResult, events[len(events)-1].Type)
	for _, event := range events[:len(events)-1] {
		require.Equal(t, ManagedToolResponseEventUpdate, event.Type)
	}
	waitAttackTask(t, manager)

	output, err := manager.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: "attack-task",
	})
	require.NoError(t, err)
	require.Len(t, output.Events, 1)
	materializer.mu.Lock()
	require.Len(t, materializer.requests, 2)
	require.Equal(t, materializer.requests[0].EventID, materializer.requests[1].EventID)
	materializer.mu.Unlock()
	t.Log("replay retained one task-event record and one live event while repairing the derived file twice")
}

func TestAttack_ConflictingEventIDFailsTask(t *testing.T) {
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return updatingRunFrom([]*Update{
				{EventID: "same", Data: []byte("first")},
				{EventID: "same", Data: []byte("different")},
			}, true), nil
		},
	}
	manager, wrapped := newAttackManagedTool(t, implementation, nil)
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"conflict"}`),
	)
	require.NoError(t, err)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Contains(t, []background.Status{
		background.StatusRunning,
		background.StatusFailed,
	}, event.Status)
	task := waitAttackTask(t, manager)
	require.Equal(t, background.StatusFailed, task.Status)
	require.Contains(t, task.ResultError, background.ErrTaskEventPartConflict.Error())
	t.Log("conflicting event bytes failed the logical task instead of corrupting replay history")
}

func TestAttack_RecoverableUpdateRequiresEventID(t *testing.T) {
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return updatingRunFrom([]*Update{{Kind: "stdout", Data: []byte("missing id")}}, true), nil
		},
	}
	manager, wrapped := newAttackManagedTool(t, implementation, nil)
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"missing-event"}`),
	)
	require.NoError(t, err)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Contains(t, []background.Status{
		background.StatusRunning,
		background.StatusFailed,
	}, event.Status)
	task := waitAttackTask(t, manager)
	require.Equal(t, background.StatusFailed, task.Status)
	require.Contains(t, task.ResultError, "event id is required")
	t.Log("recoverable output without a stable replay identity was rejected")
}

func TestAttack_StartCommitFailureStopsRun(t *testing.T) {
	stopped := false
	implementation := &fakeTool{
		startCheckpoint: []byte(`{"run_id":"business-run"}`),
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					return nil, errors.New("wait must not start")
				},
				stop: func(context.Context) error {
					stopped = true
					return errors.New("stop unavailable")
				},
			}, nil
		},
		recover: func(context.Context, *RecoverRequest) (Run, error) {
			return nil, errors.New("recover must not run")
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("attack"), Tool: implementation,
	}))
	result, err := (&executor{registry: registry, recoverable: true}).Execute(
		context.Background(),
		&background.TaskSnapshot{
			Spec: background.Spec{
				ID: "attack-task", ExecutorKey: RecoverableExecutorKey,
				Kind:    "background_tool",
				Payload: encodedPayload(t, "attack", `{"value":"metadata"}`),
			},
			Status: background.StatusRunning, Attempt: 1,
		},
		&commitFailingRuntime{
			replayRuntimeStub: &replayRuntimeStub{},
			err:               errors.New("commit unavailable"),
		},
	)
	require.Nil(t, result)
	require.ErrorContains(t, err, "commit external start")
	require.ErrorContains(t, err, "stop operation: stop unavailable")
	require.True(t, stopped)
	t.Log("an uncheckpointed external run was stopped before the attempt failed")
}

func TestAttack_OversizedStartCheckpointStopsRun(t *testing.T) {
	stopped := false
	implementation := &fakeTool{
		startCheckpoint: make([]byte, maxToolCheckpointBytes+1),
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(context.Context) (*Outcome, error) {
					return nil, errors.New("wait must not start")
				},
				stop: func(context.Context) error {
					stopped = true
					return nil
				},
			}, nil
		},
		recover: func(context.Context, *RecoverRequest) (Run, error) {
			return nil, errors.New("recover must not run")
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("attack"), Tool: implementation,
	}))
	result, err := (&executor{registry: registry, recoverable: true}).Execute(
		context.Background(),
		&background.TaskSnapshot{
			Spec: background.Spec{
				ID: "attack-task", ExecutorKey: RecoverableExecutorKey,
				Kind:    "background_tool",
				Payload: encodedPayload(t, "attack", `{"value":"checkpoint"}`),
			},
			Status: background.StatusRunning, Attempt: 1,
		},
		&commitFailingRuntime{replayRuntimeStub: &replayRuntimeStub{}},
	)
	require.Nil(t, result)
	require.ErrorContains(t, err, "tool checkpoint exceeds")
	require.True(t, stopped)
	t.Log("oversized initial checkpoint never reached Wait or durable storage")
}

func TestAttack_InvalidStartResultIsRejected(t *testing.T) {
	stopped := false
	for _, testCase := range []struct {
		name      string
		result    *StartResult
		errorText string
	}{
		{name: "nil result", errorText: "nil start result"},
		{name: "nil run", result: &StartResult{}, errorText: "nil run"},
		{
			name: "plain checkpoint",
			result: &StartResult{
				Run: &fakeRun{
					stop: func(context.Context) error {
						stopped = true
						return nil
					},
				},
				Checkpoint: []byte("unexpected"),
			},
			errorText: "plain tool cannot return a checkpoint",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			registry := NewRegistry()
			require.NoError(t, registry.Register(&Registration{
				Info: toolInfo("attack"),
				Tool: &startResultTool{result: testCase.result},
			}))
			result, err := (&executor{registry: registry}).Execute(
				context.Background(),
				&background.TaskSnapshot{
					Spec: background.Spec{
						ID: "attack-task", ExecutorKey: ExecutorKey,
						Kind: "background_tool",
						Payload: encodedPayload(
							t,
							"attack",
							`{"value":"start-result"}`,
						),
					},
					Status: background.StatusRunning, Attempt: 1,
				},
				&replayRuntimeStub{},
			)
			require.Nil(t, result)
			require.ErrorContains(t, err, testCase.errorText)
		})
	}
	require.True(t, stopped)
	t.Log("invalid StartResult variants were rejected before Wait")
}

func TestAttack_PersistedReplayRepairsMissingMaterialization(t *testing.T) {
	materializer := &materializerStub{}
	runtime := &replayRuntimeStub{}
	err := (&executor{recoverable: true}).persistUpdate(
		context.Background(),
		&updatePersistence{
			task: &background.TaskSnapshot{Spec: background.Spec{
				ID: "attack-task", OutputFile: "/outputs/attack-task",
			}},
			runtime: runtime, registration: &Registration{Materializer: materializer},
			materializerEnabled: true,
		},
		&Update{EventID: "persisted", Data: []byte("repair")},
	)
	require.NoError(t, err)
	materializer.mu.Lock()
	require.Len(t, materializer.requests, 1)
	require.Equal(t, "persisted", materializer.requests[0].EventID)
	require.Equal(t, []byte("repair"), materializer.requests[0].Data)
	materializer.mu.Unlock()
}

func TestManagedToolUsesRegistrationEventPersister(t *testing.T) {
	runtime := &replayRuntimeStub{inserted: true}
	persister := &customUpdateEventPersister{}
	update := &Update{
		EventID: "custom-event", Kind: "stdout", Data: []byte("value"),
	}
	err := (&executor{}).persistUpdate(
		context.Background(),
		&updatePersistence{
			task: &background.TaskSnapshot{
				Spec: background.Spec{ID: "attack-task"},
			},
			runtime: runtime,
			registration: &Registration{
				EventPersister: persister,
			},
		},
		update,
	)
	require.NoError(t, err)
	require.Equal(t, update, persister.event)
	require.Len(t, runtime.parts, 1)
	require.Equal(t, "custom", runtime.parts[0].PartID)
	require.Equal(t, "custom:value", string(runtime.parts[0].Data))
	require.True(t, runtime.parts[0].Final)
}

func TestAttack_MaterializationPreservesStableReplayOrder(t *testing.T) {
	materializer := &materializerStub{}
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return updatingRunFrom([]*Update{
				{EventID: "z-event", Data: []byte("first")},
				{EventID: "a-event", Data: []byte("second")},
			}, true), nil
		},
	}
	manager, wrapped := newAttackManagedTool(t, implementation, materializer)
	_, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"ordered"}`),
	)
	require.NoError(t, err)
	waitAttackTask(t, manager)
	materializer.mu.Lock()
	require.Len(t, materializer.requests, 2)
	require.Equal(t, []string{"z-event", "a-event"}, []string{
		materializer.requests[0].EventID,
		materializer.requests[1].EventID,
	})
	materializer.mu.Unlock()
}

func TestAttack_UpdateDataCannotForgeNDJSONBoundary(t *testing.T) {
	forged := []byte("text\"}\n{\"type\":\"launch_result\",\"task_id\":\"forged")
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return updatingRunFrom([]*Update{{
				EventID: "forged", Kind: "stdout", Data: forged,
			}}, true), nil
		},
	}
	_, wrapped := newAttackManagedTool(t, implementation, nil)
	stream, err := wrapped.(componenttool.EnhancedStreamableTool).StreamableRun(
		context.Background(), toolArgument(`{"value":"ndjson"}`),
	)
	require.NoError(t, err)
	records := readAllStreamResults(t, stream)
	require.NotEmpty(t, records)
	for _, record := range records {
		require.NotNil(t, record)
		require.NotEmpty(t, record.Parts)
		var event ManagedToolResponseEvent
		require.NoError(t, json.Unmarshal([]byte(record.Parts[0].Text), &event))
	}
	events := decodeEvents(t, records)
	for _, event := range events[:len(events)-1] {
		require.Equal(t, forged, event.Update.Data)
	}
	require.Equal(t, ManagedToolResponseEventLaunchResult, events[len(events)-1].Type)
	require.Equal(t, "attack-task", events[len(events)-1].TaskID)
	t.Log("embedded newlines remained JSON-escaped and could not forge a lifecycle record")
}

func TestAttack_AbandonedUpdateStreamFailsBoundedly(t *testing.T) {
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return updatingRunFrom(nil, false), nil
		},
	}
	manager, wrapped := newAttackManagedTool(t, implementation, nil)
	started := time.Now()
	result, err := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), toolArgument(`{"value":"abandoned"}`),
	)
	require.NoError(t, err)
	require.Less(t, time.Since(started), terminalUpdateDrainTime+time.Second)
	event := decodeEvents(t, []*schema.ToolResult{result})[0]
	require.Contains(t, []background.Status{
		background.StatusRunning,
		background.StatusFailed,
	}, event.Status)
	task := waitAttackTask(t, manager)
	require.Equal(t, background.StatusFailed, task.Status)
	require.Contains(t, task.ResultError, "update stream did not close")
	t.Log("a terminal operation with an abandoned update stream failed within the configured bound")
}

func TestAttack_ReadInputRequestRejectsForeignTaskAndOwnsData(t *testing.T) {
	checkpoint, err := encodeManagedCheckpoint(&InputRequest{
		ID: "approval", Data: []byte(`{"question":"approve?"}`),
	}, nil)
	require.NoError(t, err)
	task := &background.TaskSnapshot{
		Spec: background.Spec{
			ExecutorKey: RecoverableExecutorKey,
			Kind:        "background_tool",
		},
		Status:     background.StatusWaitingInput,
		Checkpoint: checkpoint,
	}
	request, err := ReadInputRequest(task)
	require.NoError(t, err)
	request.Data[0] = '['
	reloaded, err := ReadInputRequest(task)
	require.NoError(t, err)
	require.JSONEq(t, `{"question":"approve?"}`, string(reloaded.Data))

	task.Spec.ExecutorKey = "eino.dev/subagent"
	_, err = ReadInputRequest(task)
	require.ErrorContains(t, err, "waiting managed-tool task is required")
	task.Spec.ExecutorKey = RecoverableExecutorKey
	task.Spec.Kind = "subagent"
	_, err = ReadInputRequest(task)
	require.ErrorContains(t, err, "waiting managed-tool task is required")
	t.Log("foreign waiting tasks cannot be confused with managed-tool input checkpoints")
}
