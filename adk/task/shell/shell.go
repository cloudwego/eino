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

// Package shell adapts recoverable logical commands to managed background tools.
package shell

import (
	"context"
	"encoding/json"
	"errors"

	backgroundtool "github.com/cloudwego/eino/adk/task/tool"
	"github.com/cloudwego/eino/schema"
)

// RecoverableShell starts and recovers logical commands keyed by Eino task ID.
// It is intentionally separate from filesystem.Shell and StreamingShell, which
// make no cross-Worker recovery claim.
type RecoverableShell interface {
	StartCommand(context.Context, *StartCommandRequest) (backgroundtool.Run, error)
	RecoverCommand(context.Context, *RecoverCommandRequest) (backgroundtool.Run, error)
}

// StartCommandRequest describes the first attempt of a logical command.
type StartCommandRequest struct {
	TaskID  string
	Command string
	Attempt int64
}

// RecoverCommandRequest describes reconstruction of a logical command.
type RecoverCommandRequest struct {
	TaskID  string
	Command string
	Attempt int64
}

// RegistrationConfig configures a recoverable shell registration.
type RegistrationConfig struct {
	Info         *schema.ToolInfo
	Shell        RecoverableShell
	Materializer backgroundtool.OutputMaterializer
}

// NewRegistration builds a generic recoverable managed-tool registration.
func NewRegistration(config *RegistrationConfig) (*backgroundtool.Registration, error) {
	if config == nil || config.Info == nil || config.Shell == nil {
		return nil, errors.New("task/shell: tool info and recoverable shell are required")
	}
	return &backgroundtool.Registration{
		Info: config.Info, Tool: &adapter{shell: config.Shell},
		Materializer: config.Materializer,
		Description: func(arguments string) string {
			input, err := decodeArguments(arguments)
			if err != nil {
				return "Run shell command"
			}
			return input.Command
		},
	}, nil
}

type adapter struct {
	shell RecoverableShell
}

type arguments struct {
	Command string `json:"command"`
}

func (a *adapter) ValidateArguments(value string) error {
	_, err := decodeArguments(value)
	return err
}

func (a *adapter) Start(
	ctx context.Context,
	request *backgroundtool.StartRequest,
) (*backgroundtool.StartResult, error) {
	if request == nil {
		return nil, errors.New("task/shell: start request is required")
	}
	input, err := decodeArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	run, err := a.shell.StartCommand(ctx, &StartCommandRequest{
		TaskID: request.TaskID, Command: input.Command, Attempt: request.Attempt,
	})
	if err != nil {
		return nil, err
	}
	return &backgroundtool.StartResult{Run: run}, nil
}

func (a *adapter) Recover(
	ctx context.Context,
	request *backgroundtool.RecoverRequest,
) (backgroundtool.Run, error) {
	if request == nil {
		return nil, errors.New("task/shell: recover request is required")
	}
	input, err := decodeArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	return a.shell.RecoverCommand(ctx, &RecoverCommandRequest{
		TaskID: request.TaskID, Command: input.Command, Attempt: request.Attempt,
	})
}

func decodeArguments(value string) (*arguments, error) {
	var input arguments
	if err := json.Unmarshal([]byte(value), &input); err != nil {
		return nil, err
	}
	if input.Command == "" {
		return nil, errors.New("task/shell: command is required")
	}
	return &input, nil
}

var _ backgroundtool.RecoverableTool = (*adapter)(nil)
