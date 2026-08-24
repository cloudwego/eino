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

	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/compose"
)

// SubmitRequest describes a registered managed tool submitted directly as a
// durable task. Empty TaskID asks Manager to allocate one. SessionID optionally
// identifies the session notification target. It may be empty only when
// DisableLifecycleNotifications is true.
type SubmitRequest struct {
	TaskID      string
	ToolName    string
	Arguments   string
	Description string
	SessionID   string

	DisableLifecycleNotifications bool
}

// Submit validates and persists one registered managed tool without invoking
// InputPreparer or exposing the private executor payload. It installs the
// registry's executors into manager as needed. If the returned error wraps
// background.ErrTaskCreatedEventUndelivered and task is non-nil, durable
// ownership has transferred and callers must not retry Submit.
func Submit(
	ctx context.Context,
	manager *background.Manager,
	registry *Registry,
	req *SubmitRequest,
) (*background.TaskSnapshot, error) {
	if manager == nil || registry == nil || req == nil {
		return nil, errors.New(
			"task/tool: manager, tool registry, and submit request are required",
		)
	}
	if req.ToolName == "" {
		return nil, errors.New(
			"task/tool: tool name is required",
		)
	}
	if req.SessionID == "" && !req.DisableLifecycleNotifications {
		return nil, errors.New(
			"task/tool: notification session is required when lifecycle notifications are enabled",
		)
	}
	registration, recoverable, ok := registry.resolveAny(req.ToolName)
	if !ok {
		return nil, fmt.Errorf(
			"task/tool: tool %q is not registered",
			req.ToolName,
		)
	}
	if err := validateArguments(registration, req.Arguments); err != nil {
		return nil, err
	}
	if err := registerExecutors(manager, registry); err != nil {
		return nil, err
	}

	taskID := req.TaskID
	var err error
	if taskID == "" {
		taskID, err = manager.AllocateTaskID(
			ctx,
			&background.AllocateTaskIDRequest{Kind: "background_tool"},
		)
		if err != nil {
			return nil, err
		}
	} else if existing, getErr := manager.Get(ctx, taskID); getErr == nil {
		return nil, fmt.Errorf("%w: %s", background.ErrAlreadyExists, existing.Spec.ID)
	} else if !errors.Is(getErr, background.ErrNotFound) {
		return nil, getErr
	}
	outputFile := ""
	if registration.Materializer != nil {
		outputFile, err = registration.Materializer.ReserveOutput(
			ctx,
			&ReserveOutputRequest{TaskID: taskID},
		)
		if err != nil {
			return nil, fmt.Errorf("task/tool: reserve output: %w", err)
		}
		if outputFile == "" {
			return nil, errors.New(
				"task/tool: output materializer returned an empty path",
			)
		}
	}
	spec, err := buildTaskSpec(
		ctx,
		registration,
		recoverable,
		&taskSpecInput{
			taskID: taskID, arguments: req.Arguments, description: req.Description,
			outputFile: outputFile, sessionID: req.SessionID,
			notifySession: !req.DisableLifecycleNotifications,
		},
	)
	if err != nil {
		return nil, err
	}
	return manager.Submit(ctx, &background.SubmitRequest{Spec: spec})
}

func validateArguments(registration *Registration, arguments string) error {
	if arguments == "" {
		return errors.New("task/tool: arguments are required")
	}
	if len(arguments) > maxArgumentsBytes {
		return errors.New("task/tool: arguments exceed configured bounds")
	}
	if err := registration.Tool.ValidateArguments(arguments); err != nil {
		return fmt.Errorf("task/tool: validate arguments: %w", err)
	}
	return nil
}

type taskSpecInput struct {
	taskID        string
	arguments     string
	description   string
	outputFile    string
	sessionID     string
	notifySession bool
}

func buildTaskSpec(
	ctx context.Context,
	registration *Registration,
	recoverable bool,
	input *taskSpecInput,
) (background.Spec, error) {
	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, ToolName: registration.Info.Name,
		ToolCallID: compose.GetToolCallID(ctx), Arguments: input.arguments,
	})
	if err != nil {
		return background.Spec{}, fmt.Errorf(
			"task/tool: encode payload: %w",
			err,
		)
	}
	description := input.description
	if description == "" {
		description = registration.Info.Name
		if registration.Description != nil {
			description = registration.Description(input.arguments)
		}
	}
	executorKey := ExecutorKey
	if recoverable {
		executorKey = RecoverableExecutorKey
	}
	return background.Spec{
		ID: input.taskID, ExecutorKey: executorKey, Kind: "background_tool",
		Payload: payload, Description: description, OutputFile: input.outputFile,
		RootSessionID: input.sessionID, NotifySession: input.notifySession,
	}, nil
}
