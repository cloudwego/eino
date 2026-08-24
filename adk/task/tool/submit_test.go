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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task/background"
)

type submitValidationTool struct {
	validate func(string) error
	prepare  func(context.Context, string) (string, error)
}

func (t *submitValidationTool) ValidateArguments(arguments string) error {
	if t.validate != nil {
		return t.validate(arguments)
	}
	return nil
}

func (*submitValidationTool) Start(
	context.Context,
	*StartRequest,
) (*StartResult, error) {
	return nil, errors.New("not executed")
}

func (t *submitValidationTool) PrepareInput(
	ctx context.Context,
	arguments string,
) (string, error) {
	if t.prepare != nil {
		return t.prepare(ctx, arguments)
	}
	return arguments, nil
}

type countingMaterializer struct {
	reserved []string
}

func (m *countingMaterializer) ReserveOutput(
	_ context.Context,
	req *ReserveOutputRequest,
) (string, error) {
	m.reserved = append(m.reserved, req.TaskID)
	return "/outputs/" + req.TaskID, nil
}

func (*countingMaterializer) AppendOutput(
	context.Context,
	*MaterializeOutputRequest,
) error {
	return nil
}

func TestSubmitBuildsManagedToolSpecWithoutInputPreparer_BitsUT(t *testing.T) {
	prepared := false
	implementation := &submitValidationTool{
		validate: func(arguments string) error {
			require.JSONEq(t, `{"value":"input"}`, arguments)
			return nil
		},
		prepare: func(context.Context, string) (string, error) {
			prepared = true
			return `{"value":"prepared"}`, nil
		},
	}
	materializer := &countingMaterializer{}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("direct"), Tool: implementation,
		Description: func(arguments string) string {
			return "direct: " + arguments
		},
		Materializer: materializer,
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(
			context.Context,
			*background.AllocateTaskIDRequest,
		) (string, error) {
			return "allocated-task", nil
		},
	})

	task, err := Submit(context.Background(), manager, registry, &SubmitRequest{
		ToolName: "direct", Arguments: `{"value":"input"}`,
		SessionID: "parent", DisableLifecycleNotifications: true,
	})
	require.NoError(t, err)
	require.False(t, prepared)
	require.Equal(t, []string{"allocated-task"}, materializer.reserved)
	require.Equal(t, "allocated-task", task.Spec.ID)
	require.Equal(t, ExecutorKey, task.Spec.ExecutorKey)
	require.Equal(t, "background_tool", task.Spec.Kind)
	require.Equal(t, "direct: {\"value\":\"input\"}", task.Spec.Description)
	require.Equal(t, "/outputs/allocated-task", task.Spec.OutputFile)
	require.Equal(t, "parent", task.Spec.SessionID)
	require.False(t, task.Spec.NotifySession)
	var payload taskPayload
	require.NoError(t, json.Unmarshal(task.Spec.Payload, &payload))
	require.Equal(t, taskPayload{
		Version: payloadVersion, ToolName: "direct", Arguments: `{"value":"input"}`,
	}, payload)
}

func TestSubmitWithoutNotificationSession_BitsUT(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("direct"),
		Tool: &submitValidationTool{},
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-without-notification", nil
		},
	})

	task, err := Submit(context.Background(), manager, registry, &SubmitRequest{
		ToolName: "direct", Arguments: `{"value":"input"}`,
		DisableLifecycleNotifications: true,
	})
	require.NoError(t, err)
	require.Empty(t, task.Spec.SessionID)
	require.False(t, task.Spec.NotifySession)
}

func TestSubmitValidatesBeforeAllocationAndReservation_BitsUT(t *testing.T) {
	validationErr := errors.New("invalid arguments")
	materializer := &countingMaterializer{}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("direct"),
		Tool: &submitValidationTool{
			validate: func(string) error { return validationErr },
		},
		Materializer: materializer,
	}))
	allocations := 0
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(
			context.Context,
			*background.AllocateTaskIDRequest,
		) (string, error) {
			allocations++
			return "unexpected", nil
		},
	})

	_, err := Submit(context.Background(), manager, registry, &SubmitRequest{
		ToolName: "direct", Arguments: "invalid", SessionID: "parent",
	})
	require.ErrorIs(t, err, validationErr)
	require.Zero(t, allocations)
	require.Empty(t, materializer.reserved)
}

func TestSubmitRejectsInvalidRequestsAndDependencyFailures_BitsUT(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("direct"), Tool: &submitValidationTool{},
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{})
	valid := &SubmitRequest{
		TaskID: "task", ToolName: "direct", Arguments: "{}", SessionID: "parent",
	}
	for _, test := range []struct {
		name     string
		manager  *background.Manager
		registry *Registry
		req      *SubmitRequest
		err      string
	}{
		{
			name: "nil manager", registry: registry, req: valid,
			err: "task/tool: manager, tool registry, and submit request are required",
		},
		{
			name: "nil registry", manager: manager, req: valid,
			err: "task/tool: manager, tool registry, and submit request are required",
		},
		{
			name: "nil request", manager: manager, registry: registry,
			err: "task/tool: manager, tool registry, and submit request are required",
		},
		{
			name: "empty tool name", manager: manager, registry: registry,
			req: &SubmitRequest{Arguments: "{}", SessionID: "parent"},
			err: "task/tool: tool name is required",
		},
		{
			name: "empty parent session", manager: manager, registry: registry,
			req: &SubmitRequest{ToolName: "direct", Arguments: "{}"},
			err: "task/tool: notification session is required when lifecycle notifications are enabled",
		},
		{
			name: "unregistered tool", manager: manager, registry: registry,
			req: &SubmitRequest{
				ToolName: "missing", Arguments: "{}", SessionID: "parent",
			},
			err: `task/tool: tool "missing" is not registered`,
		},
		{
			name: "empty arguments", manager: manager, registry: registry,
			req: &SubmitRequest{ToolName: "direct", SessionID: "parent"},
			err: "task/tool: arguments are required",
		},
		{
			name: "large arguments", manager: manager, registry: registry,
			req: &SubmitRequest{
				ToolName: "direct", Arguments: string(make([]byte, maxArgumentsBytes+1)),
				SessionID: "parent",
			},
			err: "task/tool: arguments exceed configured bounds",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Submit(
				context.Background(), test.manager, test.registry, test.req,
			)
			require.EqualError(t, err, test.err)
		})
	}

	allocationErr := errors.New("allocation failed")
	allocationManager := mustNewBackgroundManager(
		t,
		context.Background(),
		&background.Config{
			IDGen: func(
				context.Context,
				*background.AllocateTaskIDRequest,
			) (string, error) {
				return "", allocationErr
			},
		},
	)
	_, err := Submit(
		context.Background(),
		allocationManager,
		registry,
		&SubmitRequest{
			ToolName: "direct", Arguments: "{}", SessionID: "parent",
		},
	)
	require.ErrorIs(t, err, allocationErr)

	for _, test := range []struct {
		name         string
		materializer OutputMaterializer
		err          string
	}{
		{
			name: "reservation error",
			materializer: reserveFailure{
				err: errors.New("reservation failed"),
			},
			err: "task/tool: reserve output: reservation failed",
		},
		{
			name:         "empty reservation",
			materializer: reserveFailure{},
			err:          "task/tool: output materializer returned an empty path",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			localRegistry := NewRegistry()
			require.NoError(t, localRegistry.Register(&Registration{
				Info: toolInfo("materialized"), Tool: &submitValidationTool{},
				Materializer: test.materializer,
			}))
			localManager := mustNewBackgroundManager(
				t,
				context.Background(),
				&background.Config{},
			)
			_, submitErr := Submit(
				context.Background(),
				localManager,
				localRegistry,
				&SubmitRequest{
					TaskID: "materialized-task", ToolName: "materialized",
					Arguments: "{}", SessionID: "parent",
				},
			)
			require.EqualError(t, submitErr, test.err)
		})
	}
}

func TestSubmitPreservesManagerTaskCreatedRetry_BitsUT(t *testing.T) {
	registry := NewRegistry()
	materializer := &countingMaterializer{}
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("direct"), Tool: &submitValidationTool{},
		Materializer: materializer,
	}))
	sendCalls := 0
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			sendCalls++
			if sendCalls == 1 {
				return errors.New("session unavailable")
			}
			return nil
		},
	})
	req := &SubmitRequest{
		TaskID: "stable-task", ToolName: "direct",
		Arguments: "{}", SessionID: "parent",
	}
	persisted, err := Submit(context.Background(), manager, registry, req)
	require.ErrorIs(t, err, background.ErrTaskCreatedEventUndelivered)
	require.NotNil(t, persisted)
	retried, err := Submit(context.Background(), manager, registry, req)
	require.Nil(t, retried)
	require.ErrorIs(t, err, background.ErrAlreadyExists)
	require.Equal(t, []string{"stable-task"}, materializer.reserved)

	_, err = Submit(context.Background(), manager, registry, &SubmitRequest{
		TaskID: "stable-task", ToolName: "direct",
		Arguments: `{"different":true}`, SessionID: "parent",
	})
	require.ErrorIs(t, err, background.ErrAlreadyExists)
}

func TestSubmitSelectsRecoverableExecutorAndExplicitDescription_BitsUT(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("recoverable"),
		Tool: &fakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return nil, nil
			},
			recover: func(context.Context, *RecoverRequest) (Run, error) {
				return nil, nil
			},
		},
		Description: func(string) string { return "fallback" },
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{})
	task, err := Submit(context.Background(), manager, registry, &SubmitRequest{
		TaskID: "recoverable-task", ToolName: "recoverable", Arguments: "{}",
		Description: "explicit", SessionID: "parent",
	})
	require.NoError(t, err)
	require.Equal(t, RecoverableExecutorKey, task.Spec.ExecutorKey)
	require.Equal(t, "explicit", task.Spec.Description)
	require.True(t, task.Spec.NotifySession)
}
