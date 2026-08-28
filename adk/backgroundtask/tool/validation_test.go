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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *backgroundtask.Config,
) *backgroundtask.Manager {
	t.Helper()
	if config == nil {
		config = &backgroundtask.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *backgroundtask.Task) error { return nil }
	}
	manager, err := backgroundtask.New(ctx, config)
	require.NoError(t, err)
	return manager
}

func encodedPayload(t *testing.T, name, arguments string) []byte {
	t.Helper()
	data, err := json.Marshal(&taskPayload{
		Version: payloadVersion, ToolName: name, Arguments: arguments,
	})
	require.NoError(t, err)
	return data
}

func TestRecoveryRequestsExposeCheckpoint_BitsUT(t *testing.T) {
	_, exists := reflect.TypeOf(RecoverRequest{}).FieldByName("Checkpoint")
	require.True(t, exists)
	_, exists = reflect.TypeOf(ResumeRequest{}).FieldByName("Checkpoint")
	require.True(t, exists)
}

func TestExecutorValidationBoundaries(t *testing.T) {
	registry := NewRegistry()
	plain := &plainFakeTool{start: func(context.Context, *StartRequest) (Run, error) {
		return nil, nil
	}}
	recoverable := &fakeTool{
		start:   func(context.Context, *StartRequest) (Run, error) { return nil, nil },
		recover: func(context.Context, *RecoverRequest) (Run, error) { return nil, nil },
	}
	require.NoError(t, registry.Register(&Registration{Info: toolInfo("plain"), Tool: plain}))
	require.NoError(t, registry.Register(&Registration{Info: toolInfo("recoverable"), Tool: recoverable}))
	plainExecutor := &executor{registry: registry}
	recoverableExecutor := &executor{registry: registry, recoverable: true}

	require.NoError(t, plainExecutor.ValidateSpec(backgroundtask.Spec{
		ExecutorKey: ExecutorKey, Kind: "background_tool",
		Payload: encodedPayload(t, "plain", `{}`),
	}))
	require.NoError(t, recoverableExecutor.ValidateSpec(backgroundtask.Spec{
		ExecutorKey: RecoverableExecutorKey, Kind: "background_tool",
		Payload: encodedPayload(t, "recoverable", `{}`),
	}))
	for _, spec := range []backgroundtask.Spec{
		{},
		{ExecutorKey: ExecutorKey, Kind: "wrong", Payload: encodedPayload(t, "plain", `{}`)},
		{ExecutorKey: ExecutorKey, Kind: "background_tool", Payload: []byte(`{`)},
		{ExecutorKey: ExecutorKey, Kind: "background_tool", Payload: []byte(`{"version":2}`)},
		{ExecutorKey: ExecutorKey, Kind: "background_tool", Payload: encodedPayload(t, "missing", `{}`)},
		{ExecutorKey: ExecutorKey, Kind: "background_tool", Payload: encodedPayload(t, "recoverable", `{}`)},
	} {
		require.Error(t, plainExecutor.ValidateSpec(spec))
	}
	require.Error(t, recoverableExecutor.ValidateSpec(backgroundtask.Spec{
		ExecutorKey: RecoverableExecutorKey, Kind: "background_tool",
		Payload: encodedPayload(t, "plain", `{}`),
	}))
	require.Error(t, plainExecutor.ValidateExecution(context.Background(), nil))
	require.Error(t, plainExecutor.ValidateExecution(context.Background(), &backgroundtask.Task{
		Spec: backgroundtask.Spec{
			ExecutorKey: ExecutorKey, Kind: "background_tool",
			Payload: encodedPayload(t, "missing", `{}`),
		},
	}))
	require.NoError(t, plainExecutor.ValidateExecution(context.Background(), &backgroundtask.Task{
		Spec: backgroundtask.Spec{
			ExecutorKey: ExecutorKey, Kind: "background_tool",
			Payload: encodedPayload(t, "plain", `{}`),
		},
	}))
}

func TestPayloadAndResultValidationBoundaries(t *testing.T) {
	executor := &executor{}
	oversized := strings.Repeat("x", maxArgumentsBytes+1)
	_, err := executor.decodePayload(backgroundtask.Spec{
		ExecutorKey: ExecutorKey, Kind: "background_tool",
		Payload: encodedPayload(t, "plain", oversized),
	})
	require.ErrorContains(t, err, "exceed")

	for _, update := range []*Update{
		nil,
		{Data: make([]byte, maxUpdateDataBytes+1)},
		{Kind: strings.Repeat("k", maxUpdateKindBytes+1)},
		{Metadata: map[string]string{
			"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7",
			"h": "8", "i": "9", "j": "10", "k": "11", "l": "12", "m": "13", "n": "14",
			"o": "15", "p": "16", "q": "17", "r": "18", "s": "19", "t": "20",
			"u": "21", "v": "22", "w": "23", "x": "24", "y": "25", "z": "26",
			"aa": "27", "ab": "28", "ac": "29", "ad": "30", "ae": "31", "af": "32",
			"ag": "33",
		}},
		{Metadata: map[string]string{"": "value"}},
		{Metadata: map[string]string{"key": strings.Repeat("v", maxUpdateMetadataBytes+1)}},
	} {
		if update == nil {
			continue
		}
		require.Error(t, validateUpdate(update))
	}
	require.NoError(t, validateUpdate(&Update{Metadata: map[string]string{"key": "value"}}))

	valid, err := validateOutcome(&Outcome{
		Status: backgroundtask.StatusCompleted, Data: []byte("done"),
	}, false, nil)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCompleted, valid.Status)
	waiting, err := validateOutcome(&Outcome{
		Status: backgroundtask.StatusWaitingInput,
		InputRequest: &InputRequest{
			ID: "approval", Data: []byte(`{"question":"approve?"}`),
		},
	}, true, []byte("current"))
	require.NoError(t, err)
	require.NotEmpty(t, waiting.Checkpoint)
	_, retained, _, err := decodeManagedCheckpoint(waiting.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, "current", string(retained))
	waiting, err = validateOutcome(&Outcome{
		Status: backgroundtask.StatusWaitingInput,
		InputRequest: &InputRequest{
			ID: "approval", Data: []byte(`{"question":"approve?"}`),
		},
		Checkpoint: []byte("next"),
	}, true, []byte("current"))
	require.NoError(t, err)
	_, replaced, _, err := decodeManagedCheckpoint(waiting.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, "next", string(replaced))
	toolCheckpoint := []byte("business-run")
	checkpoint, err := encodeManagedCheckpoint(nil, toolCheckpoint)
	require.NoError(t, err)
	toolCheckpoint[0] = 'X'
	request, decodedCheckpoint, started, err := decodeManagedCheckpoint(checkpoint)
	require.NoError(t, err)
	require.Nil(t, request)
	require.True(t, started)
	require.Equal(t, "business-run", string(decodedCheckpoint))
	decodedCheckpoint[0] = 'Y'
	_, decodedAgain, _, err := decodeManagedCheckpoint(checkpoint)
	require.NoError(t, err)
	require.Equal(t, "business-run", string(decodedAgain))
	for _, testCase := range []struct {
		name    string
		request *InputRequest
	}{
		{
			name: "oversized id",
			request: &InputRequest{
				ID: strings.Repeat("i", maxInputRequestIDBytes+1),
			},
		},
		{
			name: "oversized data",
			request: &InputRequest{
				ID: "request",
				Data: []byte(
					`"` + strings.Repeat("d", maxInputRequestDataBytes) + `"`,
				),
			},
		},
		{
			name: "invalid json",
			request: &InputRequest{
				ID:   "request",
				Data: []byte(`{`),
			},
		},
	} {
		t.Run("input checkpoint "+testCase.name, func(t *testing.T) {
			_, encodeErr := encodeManagedCheckpoint(testCase.request, nil)
			require.Error(t, encodeErr)
		})
	}
	_, err = encodeManagedCheckpoint(
		nil,
		make([]byte, maxToolCheckpointBytes+1),
	)
	require.ErrorContains(t, err, "tool checkpoint exceeds")
	for _, testCase := range []struct {
		outcome        *Outcome
		supportsResume bool
	}{
		{outcome: nil},
		{outcome: &Outcome{Status: backgroundtask.StatusCompleted, Error: "bad"}},
		{outcome: &Outcome{
			Status: backgroundtask.StatusCompleted, Checkpoint: []byte("bad"),
		}},
		{outcome: &Outcome{Status: backgroundtask.StatusFailed}},
		{outcome: &Outcome{
			Status: backgroundtask.StatusFailed, Error: "bad", Data: []byte("bad"),
		}},
		{outcome: &Outcome{
			Status: backgroundtask.StatusFailed, Error: "bad",
			Checkpoint: []byte("bad"),
		}},
		{outcome: &Outcome{
			Status: backgroundtask.StatusCanceled, Data: []byte("bad"),
		}},
		{outcome: &Outcome{
			Status: backgroundtask.StatusCanceled, Checkpoint: []byte("bad"),
		}},
		{outcome: &Outcome{Status: backgroundtask.StatusRunning}},
		{outcome: &Outcome{
			Status:       backgroundtask.StatusWaitingInput,
			InputRequest: &InputRequest{ID: "approval"},
		}},
		{outcome: &Outcome{
			Status: backgroundtask.StatusWaitingInput,
		}, supportsResume: true},
		{outcome: &Outcome{
			Status:       backgroundtask.StatusWaitingInput,
			Data:         []byte("terminal"),
			InputRequest: &InputRequest{ID: "approval"},
		}, supportsResume: true},
		{outcome: &Outcome{
			Status:       backgroundtask.StatusWaitingInput,
			InputRequest: &InputRequest{ID: "approval"},
			Checkpoint:   make([]byte, maxToolCheckpointBytes+1),
		}, supportsResume: true},
	} {
		_, err = validateOutcome(testCase.outcome, testCase.supportsResume, nil)
		require.Error(t, err)
	}
}

func TestEncodeEventRejectsMixedVariants_BitsUT(t *testing.T) {
	for _, event := range []*ManagedToolResponseEvent{
		nil,
		{Type: ManagedToolResponseEventUpdate},
		{Type: ManagedToolResponseEventUpdate, Update: &Update{}, TaskID: "task"},
		{Type: ManagedToolResponseEventLaunchResult},
		{
			Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
			Status: backgroundtask.StatusRunning, Output: "not terminal",
		},
		{
			Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
			Status: backgroundtask.StatusCompleted, Error: "bad",
		},
	} {
		_, err := encodeEvent(event)
		require.Error(t, err)
	}
}

type reserveFailure struct {
	path string
	err  error
}

func (m reserveFailure) ReserveOutput(context.Context, *ReserveOutputRequest) (string, error) {
	return m.path, m.err
}
func (reserveFailure) AppendOutput(context.Context, *MaterializeOutputRequest) error { return nil }

func TestManagedToolConstructionAndSubmissionErrors(t *testing.T) {
	_, err := NewManagedTool(context.Background(), nil)
	require.Error(t, err)
	registry := NewRegistry()
	executors := backgroundtask.NewExecutorRegistry()
	_, err = NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager:   mustNewBackgroundManager(t, context.Background(), nil),
		Executors: executors,
		Registry:  registry, ToolName: "missing",
	})
	require.ErrorContains(t, err, `tool "missing" is not registered`)

	plain := &plainFakeTool{start: func(context.Context, *StartRequest) (Run, error) {
		return &fakeRun{wait: func(context.Context) (*Outcome, error) {
			return &Outcome{Status: backgroundtask.StatusCompleted}, nil
		}}, nil
	}}
	require.NoError(t, registry.Register(&Registration{Info: toolInfo("plain"), Tool: plain}))
	executors = backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{Executors: executors})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "plain",
		SessionID: func(context.Context) (string, error) {
			return "", errors.New("session unavailable")
		},
	})
	require.NoError(t, err)
	_, err = wrapped.Info(context.Background())
	require.NoError(t, err)
	_, err = wrapped.(*managedTool).InvokableRun(context.Background(), nil)
	require.ErrorContains(t, err, "tool argument is required")
	_, err = wrapped.(*managedTool).InvokableRun(context.Background(), toolArgument(""))
	require.ErrorContains(t, err, "arguments are required")
	_, err = wrapped.(*managedTool).InvokableRun(
		context.Background(), toolArgument(strings.Repeat("x", maxArgumentsBytes+1)),
	)
	require.ErrorContains(t, err, "arguments exceed")
	_, err = wrapped.(*managedTool).InvokableRun(context.Background(), toolArgument(`{`))
	require.ErrorContains(t, err, "validate arguments")
	_, err = wrapped.(*managedTool).InvokableRun(context.Background(), toolArgument(`{}`))
	require.ErrorContains(t, err, "session unavailable")

	for _, testCase := range []struct {
		materializer OutputMaterializer
		errorText    string
	}{
		{materializer: reserveFailure{err: errors.New("reserve failed")}, errorText: "reserve failed"},
		{materializer: reserveFailure{}, errorText: "empty path"},
	} {
		localRegistry := NewRegistry()
		require.NoError(t, localRegistry.Register(&Registration{
			Info: toolInfo("materialized"), Tool: plain, Materializer: testCase.materializer,
		}))
		localExecutors := backgroundtask.NewExecutorRegistry()
		localManager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
			Executors: localExecutors,
		})
		localWrapped, createErr := NewManagedTool(context.Background(), &ManagedToolConfig{
			Manager: localManager, Executors: localExecutors,
			Registry: localRegistry, ToolName: "materialized",
			SessionID: func(context.Context) (string, error) { return "session", nil },
		})
		require.NoError(t, createErr)
		_, runErr := localWrapped.(*managedTool).InvokableRun(
			context.Background(), toolArgument(`{}`),
		)
		require.ErrorContains(t, runErr, testCase.errorText)
	}
}

func TestFormattingAndProjectionHelpers(t *testing.T) {
	require.Equal(t, "[update]", formatProgressEvent(&backgroundtask.TaskEvent{
		Data: []byte(`{"event_id":"id"}`),
	}))
	require.Contains(t, formatProgressEvent(&backgroundtask.TaskEvent{
		Data: []byte(`{"event_id":"id","metadata":{"artifact":"ref"}}`),
	}), "artifact")
	require.Equal(t, "[update] hello", formatProgressEvent(&backgroundtask.TaskEvent{
		Data: []byte(`{"event_id":"id","data":"aGVsbG8="}`),
	}))

	require.Nil(t, cloneUpdate(nil))
	update := &Update{Data: []byte("data"), Metadata: map[string]string{"key": "value"}}
	cloned := cloneUpdate(update)
	update.Data[0] = 'X'
	update.Metadata["key"] = "changed"
	require.Equal(t, "data", string(cloned.Data))
	require.Equal(t, "value", cloned.Metadata["key"])

	projection := newLiveProjection()
	projection.detach()
	projection.send(context.Background(), nil, &Update{EventID: "ignored"})
	select {
	case <-projection.updates:
		t.Fatal("detached projection accepted an update")
	default:
	}
}

func TestCloneToolInfoRejectsInvalidMetadata(t *testing.T) {
	_, err := cloneToolInfo(nil)
	require.Error(t, err)
	_, err = cloneToolInfo(&schema.ToolInfo{
		Name: "invalid", Extra: map[string]any{"channel": make(chan struct{})},
	})
	require.Error(t, err)
}

func TestManagedToolTimeoutOverrideStopsRun(t *testing.T) {
	stopped := make(chan struct{})
	implementation := &fakeTool{
		start: func(context.Context, *StartRequest) (Run, error) {
			return &fakeRun{
				wait: func(ctx context.Context) (*Outcome, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				},
				stop: func(context.Context) error {
					close(stopped)
					return nil
				},
			}, nil
		},
	}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("timeout"), Tool: implementation,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{Executors: executors})
	timeoutMs := 5
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Executors: executors, Registry: registry, ToolName: "timeout",
		InvocationTimeoutMs: func(context.Context, string) *int { return &timeoutMs },
		SessionID:           func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	result, err := wrapped.(*managedTool).InvokableRun(
		context.Background(), toolArgument(`{}`),
	)
	require.Nil(t, result)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var timeoutErr *backgroundtask.ForegroundTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, 5*time.Millisecond, timeoutErr.Timeout)
	require.NotEmpty(t, timeoutErr.TaskID)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("timeout did not stop the logical operation")
	}
}

func TestManagedToolProjectionErrors(t *testing.T) {
	newManaged := func(registration *Registration) *managedTool {
		return &managedTool{
			registry:     newRegistryWithProjection(),
			registration: registration,
		}
	}
	runProject := func(
		t *testing.T,
		managed *managedTool,
		ctx context.Context,
		taskID string,
		result launchResult,
	) ([]*schema.ToolResult, error) {
		t.Helper()
		projection, err := managed.registry.projections.register(taskID)
		require.NoError(t, err)
		if result.task != nil && result.task.Status != backgroundtask.StatusRunning {
			projection.closeUpdates()
		}
		reader, writer := schema.Pipe[*schema.ToolResult](2)
		done := make(chan launchResult, 1)
		done <- result
		go managed.project(ctx, taskID, projection, done, writer)
		return receiveProjectResult(t, reader)
	}
	registration := &Registration{Info: toolInfo("project")}
	managed := newManaged(registration)
	_, err := runProject(t, managed, context.Background(), "run-error", launchResult{
		err: errors.New("run failed"),
	})
	require.ErrorContains(t, err, "run failed")

	managed = newManaged(registration)
	_, err = runProject(t, managed, context.Background(), "nil-task", launchResult{})
	require.ErrorContains(t, err, "nil task")

	invalidRegistration := &Registration{
		Info: toolInfo("project"),
		RenderResult: func(context.Context, *backgroundtask.Task) (*schema.ToolResult, error) {
			return nil, errors.New("render failed")
		},
	}
	managed = newManaged(invalidRegistration)
	_, err = runProject(t, managed, context.Background(), "invalid-final", launchResult{
		task: &backgroundtask.Task{
			Spec:   backgroundtask.Spec{ID: "invalid-final"},
			Status: backgroundtask.StatusCompleted,
		},
	})
	require.ErrorContains(t, err, "render failed")

	managed = newManaged(registration)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	projection, err := managed.registry.projections.register("canceled")
	require.NoError(t, err)
	reader, writer := schema.Pipe[*schema.ToolResult](1)
	go managed.project(
		canceledCtx, "canceled", projection, make(chan launchResult), writer,
	)
	_, err = receiveProjectResult(t, reader)
	require.ErrorIs(t, err, context.Canceled)

	managed = newManaged(registration)
	projection, err = managed.registry.projections.register("closed-reader")
	require.NoError(t, err)
	reader, writer = schema.Pipe[*schema.ToolResult](1)
	reader.Close()
	projection.updates <- &Update{EventID: "ignored"}
	go managed.project(
		context.Background(), "closed-reader", projection,
		make(chan launchResult), writer,
	)
	require.Eventually(t, func() bool {
		return managed.registry.projections.load("closed-reader") == nil
	}, time.Second, time.Millisecond)
}

func newRegistryWithProjection() *Registry {
	return NewRegistry()
}

func receiveProjectResult(
	t *testing.T,
	reader *schema.StreamReader[*schema.ToolResult],
) ([]*schema.ToolResult, error) {
	t.Helper()
	defer reader.Close()
	var records []*schema.ToolResult
	for {
		record, err := reader.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return records, nil
			}
			return records, err
		}
		records = append(records, record)
	}
}
