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
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *background.Config,
) *background.Manager {
	t.Helper()
	if config == nil {
		config = &background.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *background.TaskSnapshot) error { return nil }
	}
	manager, err := background.New(ctx, config)
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

func TestPersistedExecutorKeysRemainCompatible(t *testing.T) {
	require.Equal(t, "eino.dev/background-tool", ExecutorKey)
	require.Equal(t, "eino.dev/recoverable-background-tool", RecoverableExecutorKey)
}

func TestRegisterExecutors(t *testing.T) {
	registry := NewRegistry()
	require.EqualError(
		t,
		registerExecutors(nil, registry),
		"task/tool: manager and tool registry are required",
	)
	require.EqualError(
		t,
		registerExecutors(mustNewBackgroundManager(t, context.Background(), nil), nil),
		"task/tool: manager and tool registry are required",
	)

	t.Run("registers both executor policies idempotently", func(t *testing.T) {
		manager := mustNewBackgroundManager(t, context.Background(), nil)
		require.NoError(t, registerExecutors(manager, registry))
		require.NoError(t, registerExecutors(manager, registry))

		for _, recoverable := range []bool{false, true} {
			actual, loaded, err := manager.LoadOrRegisterExecutor(&executor{
				registry: registry, recoverable: recoverable,
			})
			require.NoError(t, err)
			require.True(t, loaded)
			registered, ok := actual.(*executor)
			require.True(t, ok)
			require.Same(t, registry, registered.registry)
			require.Equal(t, recoverable, registered.recoverable)
		}
	})

	t.Run("rejects an incompatible existing registration", func(t *testing.T) {
		manager := mustNewBackgroundManager(t, context.Background(), nil)
		_, loaded, err := manager.LoadOrRegisterExecutor(&executor{
			registry: NewRegistry(),
		})
		require.NoError(t, err)
		require.False(t, loaded)

		err = registerExecutors(manager, registry)
		require.EqualError(
			t,
			err,
			`task/tool: executor key "eino.dev/background-tool" is already registered incompatibly`,
		)
	})
}

func TestRecoveryRequestsExposeCheckpoint_BitsUT(t *testing.T) {
	_, exists := reflect.TypeOf(RecoverRequest{}).FieldByName("Checkpoint")
	require.True(t, exists)
	_, exists = reflect.TypeOf(ResumeRequest{}).FieldByName("Checkpoint")
	require.True(t, exists)
}

func TestExecutorValidateSpec(t *testing.T) {
	registry := NewRegistry()
	plain := &plainFakeTool{}
	recoverable := &fakeTool{}
	validationErr := errors.New("arguments rejected")
	validating := &submitValidationTool{
		validate: func(arguments string) error {
			require.Equal(t, `{"value":"rejected"}`, arguments)
			return validationErr
		},
	}
	require.NoError(t, registry.Register(&Registration{Info: toolInfo("plain"), Tool: plain}))
	require.NoError(t, registry.Register(&Registration{Info: toolInfo("recoverable"), Tool: recoverable}))
	require.NoError(t, registry.Register(&Registration{Info: toolInfo("validating"), Tool: validating}))
	registry.plain["recoverable-in-plain"] = &Registration{
		Info: toolInfo("recoverable-in-plain"), Tool: recoverable,
	}
	registry.recoverable["plain-in-recoverable"] = &Registration{
		Info: toolInfo("plain-in-recoverable"), Tool: plain,
	}
	plainExecutor := &executor{registry: registry}
	recoverableExecutor := &executor{registry: registry, recoverable: true}
	spec := func(executorKey, toolName, arguments string) background.Spec {
		return background.Spec{
			ExecutorKey: executorKey,
			Kind:        "background_tool",
			Payload:     encodedPayload(t, toolName, arguments),
		}
	}

	require.EqualError(
		t,
		plainExecutor.ValidateSpec(background.Spec{}),
		"task/tool: invalid executor key or task kind",
	)
	require.EqualError(
		t,
		plainExecutor.ValidateSpec(background.Spec{
			ExecutorKey: ExecutorKey, Kind: "background_tool", Payload: []byte(`{`),
		}),
		"task/tool: decode payload: unexpected end of JSON input",
	)
	err := plainExecutor.ValidateSpec(background.Spec{
		ExecutorKey: ExecutorKey,
		Kind:        "background_tool",
		Payload:     []byte(`{"version":2}`),
	})
	require.EqualError(
		t,
		err,
		"task/background: unsupported executor payload version: managed-tool payload version 2",
	)
	require.ErrorIs(t, err, background.ErrUnsupportedExecutorPayloadVersion)
	require.EqualError(
		t,
		plainExecutor.ValidateSpec(spec(ExecutorKey, "plain", "")),
		"task/tool: payload tool name and arguments are required",
	)
	require.EqualError(
		t,
		plainExecutor.ValidateSpec(spec(ExecutorKey, "missing", `{}`)),
		`task/tool: tool "missing" is not registered for executor "eino.dev/background-tool"`,
	)
	require.EqualError(
		t,
		plainExecutor.ValidateSpec(spec(ExecutorKey, "recoverable-in-plain", `{}`)),
		`task/tool: recoverable tool "recoverable-in-plain" used plain executor`,
	)
	require.EqualError(
		t,
		recoverableExecutor.ValidateSpec(
			spec(RecoverableExecutorKey, "plain-in-recoverable", `{}`),
		),
		`task/tool: tool "plain-in-recoverable" is not recoverable`,
	)
	require.ErrorIs(
		t,
		plainExecutor.ValidateSpec(spec(ExecutorKey, "validating", `{"value":"rejected"}`)),
		validationErr,
	)
	require.NoError(t, plainExecutor.ValidateSpec(spec(ExecutorKey, "plain", `{}`)))
	require.NoError(
		t,
		recoverableExecutor.ValidateSpec(
			spec(RecoverableExecutorKey, "recoverable", `{}`),
		),
	)
}

func TestExecutorValidateExecution(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("plain"), Tool: &plainFakeTool{},
	}))
	plainExecutor := &executor{registry: registry}

	require.EqualError(
		t,
		plainExecutor.ValidateExecution(context.Background(), nil),
		"task/tool: task is required",
	)
	require.EqualError(t, plainExecutor.ValidateExecution(
		context.Background(),
		&background.TaskSnapshot{
			Spec: background.Spec{
				ExecutorKey: ExecutorKey, Kind: "background_tool",
				Payload: encodedPayload(t, "missing", `{}`),
			},
		},
	), `task/tool: tool "missing" is unavailable`)
	require.NoError(t, plainExecutor.ValidateExecution(context.Background(), &background.TaskSnapshot{
		Spec: background.Spec{
			ExecutorKey: ExecutorKey, Kind: "background_tool",
			Payload: encodedPayload(t, "plain", `{}`),
		},
	}))
}

func TestEncodeManagedCheckpointAtCursor(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		request    *InputRequest
		checkpoint []byte
		cursor     int64
		wantErr    string
	}{
		{
			name:    "negative cursor",
			cursor:  -1,
			wantErr: "task/tool: input cursor is invalid",
		},
		{
			name:       "oversized tool checkpoint",
			checkpoint: make([]byte, maxToolCheckpointBytes+1),
			wantErr:    "task/tool: tool checkpoint exceeds configured bounds",
		},
		{
			name:    "empty request ID",
			request: &InputRequest{},
			wantErr: "task/tool: waiting-input outcome requires an input request ID",
		},
		{
			name:    "oversized request ID",
			request: &InputRequest{ID: strings.Repeat("i", maxInputRequestIDBytes+1)},
			wantErr: "task/tool: input request ID exceeds configured bounds",
		},
		{
			name: "oversized request data",
			request: &InputRequest{
				ID: "approval", Data: make([]byte, maxInputRequestDataBytes+1),
			},
			wantErr: "task/tool: input request data exceeds configured bounds",
		},
		{
			name:    "invalid request JSON",
			request: &InputRequest{ID: "approval", Data: []byte(`{`)},
			wantErr: "task/tool: input request data must be valid JSON",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			data, err := encodeManagedCheckpointAtCursor(
				testCase.request,
				testCase.checkpoint,
				testCase.cursor,
			)
			require.Nil(t, data)
			require.EqualError(t, err, testCase.wantErr)
		})
	}

	request := &InputRequest{ID: "approval", Data: json.RawMessage(`{"answer":true}`)}
	toolCheckpoint := []byte("opaque")
	data, err := encodeManagedCheckpointAtCursor(request, toolCheckpoint, 7)
	require.NoError(t, err)
	var checkpoint managedCheckpoint
	require.NoError(t, json.Unmarshal(data, &checkpoint))
	require.Equal(t, managedCheckpointVersion, checkpoint.Version)
	require.True(t, checkpoint.Started)
	require.Equal(t, int64(7), checkpoint.InputCursor)
	require.Equal(t, []byte("opaque"), checkpoint.ToolCheckpoint)
	require.Equal(t, "approval", checkpoint.Request.ID)
	require.JSONEq(t, `{"answer":true}`, string(checkpoint.Request.Data))
}

func TestPayloadAndResultValidationBoundaries(t *testing.T) {
	executor := &executor{}
	oversized := strings.Repeat("x", maxArgumentsBytes+1)
	_, err := executor.decodePayload(background.Spec{
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
		Status: task.OutcomeCompleted, Data: []byte("done"),
	}, false, nil, 0)
	require.NoError(t, err)
	require.Equal(t, background.ExecutionActionComplete, valid.Action)
	waiting, err := validateOutcome(&Outcome{
		Status: task.OutcomeInterrupted,
		InputRequest: &InputRequest{
			ID: "approval", Data: []byte(`{"question":"approve?"}`),
		},
	}, true, []byte("current"), 0)
	require.NoError(t, err)
	require.NotEmpty(t, waiting.Checkpoint)
	_, retained, _, err := decodeManagedCheckpoint(waiting.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, "current", string(retained))
	waiting, err = validateOutcome(&Outcome{
		Status: task.OutcomeInterrupted,
		InputRequest: &InputRequest{
			ID: "approval", Data: []byte(`{"question":"approve?"}`),
		},
		Checkpoint: []byte("next"),
	}, true, []byte("current"), 0)
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
		{outcome: &Outcome{Status: task.OutcomeCompleted, Error: "bad"}},
		{outcome: &Outcome{
			Status: task.OutcomeCompleted, Checkpoint: []byte("bad"),
		}},
		{outcome: &Outcome{Status: task.OutcomeFailed}},
		{outcome: &Outcome{
			Status: task.OutcomeFailed, Error: "bad", Data: []byte("bad"),
		}},
		{outcome: &Outcome{
			Status: task.OutcomeFailed, Error: "bad",
			Checkpoint: []byte("bad"),
		}},
		{outcome: &Outcome{
			Status: task.OutcomeCanceled, Data: []byte("bad"),
		}},
		{outcome: &Outcome{
			Status: task.OutcomeCanceled, Checkpoint: []byte("bad"),
		}},
		{outcome: &Outcome{Status: task.OutcomeStatus(99)}},
		{outcome: &Outcome{
			Status:       task.OutcomeInterrupted,
			InputRequest: &InputRequest{ID: "approval"},
		}},
		{outcome: &Outcome{
			Status: task.OutcomeInterrupted,
		}, supportsResume: true},
		{outcome: &Outcome{
			Status:       task.OutcomeInterrupted,
			Data:         []byte("terminal"),
			InputRequest: &InputRequest{ID: "approval"},
		}, supportsResume: true},
		{outcome: &Outcome{
			Status:       task.OutcomeInterrupted,
			InputRequest: &InputRequest{ID: "approval"},
			Checkpoint:   make([]byte, maxToolCheckpointBytes+1),
		}, supportsResume: true},
	} {
		_, err = validateOutcome(testCase.outcome, testCase.supportsResume, nil, 0)
		require.Error(t, err)
	}
}

func TestEncodeEventRejectsMixedVariants_BitsUT(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		event *ManagedToolResponseEvent
		err   string
	}{
		{
			name: "nil",
			err:  "task/tool: response event is required",
		},
		{
			name:  "unknown type",
			event: &ManagedToolResponseEvent{Type: "unknown"},
			err:   "task/tool: unknown response event type",
		},
		{
			name:  "update missing payload",
			event: &ManagedToolResponseEvent{Type: ManagedToolResponseEventUpdate},
			err:   "task/tool: invalid update response event",
		},
		{
			name: "update mixed with result fields",
			event: &ManagedToolResponseEvent{
				Type: ManagedToolResponseEventUpdate, Update: &Update{},
				TaskID: "task", Status: background.StatusRunning,
				Description: "work", Output: "output", Error: "error",
				InputRequest: &InputRequest{ID: "request"},
			},
			err: "task/tool: invalid update response event",
		},
		{
			name:  "launch missing id and status",
			event: &ManagedToolResponseEvent{Type: ManagedToolResponseEventLaunchResult},
			err:   "task/tool: invalid launch-result response event",
		},
		{
			name: "launch mixed with update",
			event: &ManagedToolResponseEvent{
				Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
				Status: background.StatusRunning, Update: &Update{},
			},
			err: "task/tool: invalid launch-result response event",
		},
		{
			name: "launch non-completed output",
			event: &ManagedToolResponseEvent{
				Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
				Status: background.StatusRunning, Output: "not terminal",
			},
			err: "task/tool: non-completed launch result cannot contain output",
		},
		{
			name: "launch completed error",
			event: &ManagedToolResponseEvent{
				Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
				Status: background.StatusCompleted, Error: "bad",
			},
			err: "task/tool: completed launch result cannot contain error",
		},
		{
			name: "launch waiting without request",
			event: &ManagedToolResponseEvent{
				Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
				Status: background.StatusWaitingInput,
			},
			err: "task/tool: waiting launch result requires only an input request",
		},
		{
			name: "launch non-waiting request",
			event: &ManagedToolResponseEvent{
				Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
				Status:       background.StatusRunning,
				InputRequest: &InputRequest{ID: "request"},
			},
			err: "task/tool: non-waiting launch result cannot contain an input request",
		},
		{
			name: "foreground exposes task id",
			event: &ManagedToolResponseEvent{
				Type:   ManagedToolResponseEventForegroundResult,
				TaskID: "task", Status: background.StatusCompleted,
			},
			err: "task/tool: invalid foreground-result response event",
		},
		{
			name: "foreground non-completed output",
			event: &ManagedToolResponseEvent{
				Type:   ManagedToolResponseEventForegroundResult,
				Status: background.StatusFailed, Output: "partial",
			},
			err: "task/tool: non-completed foreground result cannot contain output",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := encodeEvent(testCase.event)
			require.EqualError(t, err, testCase.err)
		})
	}

	encoded, err := encodeEvent(&ManagedToolResponseEvent{
		Type: ManagedToolResponseEventLaunchResult, TaskID: "task",
		Status: background.StatusRunning, Description: "work",
	})
	require.NoError(t, err)
	require.Equal(
		t,
		"{\"type\":\"launch_result\",\"task_id\":\"task\",\"status\":\"running\",\"description\":\"work\"}\n",
		encoded,
	)
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
	_, err = NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager:  mustNewBackgroundManager(t, context.Background(), nil),
		Registry: registry, ToolName: "missing",
	})
	require.ErrorContains(t, err, `tool "missing" is not registered`)

	plain := &plainFakeTool{start: func(context.Context, *StartRequest) (Run, error) {
		return &fakeRun{wait: func(context.Context) (*Outcome, error) {
			return &Outcome{Status: task.OutcomeCompleted}, nil
		}}, nil
	}}
	require.NoError(t, registry.Register(&Registration{Info: toolInfo("plain"), Tool: plain}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{})
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "plain",
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
		localManager := mustNewBackgroundManager(t, context.Background(), &background.Config{})
		localWrapped, createErr := NewManagedTool(context.Background(), &ManagedToolConfig{
			Manager: localManager, Registry: localRegistry, ToolName: "materialized",
			SessionID: func(context.Context) (string, error) { return "session", nil },
		})
		require.NoError(t, createErr)
		_, runErr := localWrapped.(*managedTool).InvokableRun(
			context.Background(), toolArgument(`{}`),
		)
		require.ErrorContains(t, runErr, testCase.errorText)
	}
}

func TestFormatProgressEvent(t *testing.T) {
	require.Empty(t, formatProgressEvent(nil))
	require.Equal(t, "not-json", formatProgressEvent(&background.TaskEventPart{
		Data: []byte("not-json"),
	}))
	require.Equal(t, "[update]", formatProgressEvent(&background.TaskEventPart{
		Data: []byte(`{"event_id":"id"}`),
	}))
	require.Equal(t, `[update] {"artifact":"ref"}`, formatProgressEvent(
		&background.TaskEventPart{
			Data: []byte(`{"event_id":"id","metadata":{"artifact":"ref"}}`),
		},
	))
	require.Equal(t, "[stdout] hello", formatProgressEvent(&background.TaskEventPart{
		Data: []byte(`{"event_id":"id","kind":"stdout","data":"aGVsbG8="}`),
	}))
	raw := strings.Repeat("x", 1025)
	require.Equal(
		t,
		strings.Repeat("x", 1024)+"...[truncated]",
		formatProgressEvent(&background.TaskEventPart{Data: []byte(raw)}),
	)
}

func TestProjectionHelpers(t *testing.T) {
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
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{})
	timeoutMs := 5
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "timeout",
		ForegroundTimeoutMsForInvocation: func(context.Context, string) *int {
			return &timeoutMs
		},
		SessionID: func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	result, err := wrapped.(*managedTool).InvokableRun(
		context.Background(), toolArgument(`{}`),
	)
	require.Nil(t, result)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var timeoutErr *task.ForegroundTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, 5*time.Millisecond, timeoutErr.Timeout)
	require.NotEmpty(t, timeoutErr.TaskID)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("timeout did not stop the logical operation")
	}
}
