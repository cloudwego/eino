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

package filesystem

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	backgroundshell "github.com/cloudwego/eino/adk/backgroundtask/shell"
	backgroundtool "github.com/cloudwego/eino/adk/backgroundtask/tool"
	componenttool "github.com/cloudwego/eino/components/tool"
)

type recoverableShellStub struct {
	startRequest   *backgroundshell.StartCommandRequest
	recoverRequest *backgroundshell.RecoverCommandRequest
}

func (*recoverableShellStub) ValidateCheckpoint([]byte) error { return nil }
func (s *recoverableShellStub) StartCommand(
	_ context.Context,
	request *backgroundshell.StartCommandRequest,
) (backgroundtool.Run, error) {
	s.startRequest = request
	return recoverableShellRun{}, nil
}
func (s *recoverableShellStub) RecoverCommand(
	_ context.Context,
	request *backgroundshell.RecoverCommandRequest,
) (backgroundtool.Run, error) {
	s.recoverRequest = request
	return recoverableShellRun{}, nil
}

type recoverableShellRun struct{}

func (recoverableShellRun) Wait(context.Context) (*backgroundtool.Outcome, error) {
	return &backgroundtool.Outcome{
		Status: backgroundtask.StatusCompleted, Data: []byte("command output"),
	}, nil
}
func (recoverableShellRun) Stop(context.Context) error { return nil }

func TestRecoverableShellUsesManagedToolLifecycle(t *testing.T) {
	shell := &recoverableShellStub{}
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "shell-task", nil
		},
	})
	config := &MiddlewareConfig{
		RecoverableShell: shell,
		Background: &BackgroundConfig{
			Manager: manager, Notifications: testNotifications,
		},
		notificationSessionID: func(context.Context) (string, error) {
			return "session", nil
		},
	}
	tools, err := getFilesystemTools(context.Background(), config)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	result, err := tools[0].(componenttool.InvokableTool).InvokableRun(
		context.Background(), `{"command":"echo hello"}`,
	)
	require.NoError(t, err)
	var event backgroundtool.ToolStreamEvent
	require.NoError(t, json.Unmarshal([]byte(result), &event))
	require.Equal(t, "launch_result", event.Type)
	require.Equal(t, "shell-task", event.TaskID)
	require.Equal(t, backgroundtask.StatusCompleted, event.Status)
	require.Equal(t, "command output", event.Output)
	require.NotNil(t, shell.startRequest)
	require.Equal(t, "shell-task", shell.startRequest.TaskID)
	require.Equal(t, "echo hello", shell.startRequest.Command)

	task, err := manager.Get(context.Background(), "shell-task")
	require.NoError(t, err)
	require.Equal(t, backgroundtool.RecoverableExecutorKey, task.Spec.ExecutorKey)
	require.Empty(t, task.Spec.OutputFile)
	require.Equal(t, `{"command":"echo hello"}`, backgroundtool.ArgumentsFromTask(task))
}

func TestRecoverableShellConfigurationIsExclusive(t *testing.T) {
	config := &MiddlewareConfig{
		Shell:            &mockShellBackend{},
		RecoverableShell: &recoverableShellStub{},
		Background: &BackgroundConfig{
			Manager: backgroundtask.New(context.Background(), nil),
		},
	}
	require.ErrorContains(t, config.Validate(), "mutually exclusive")

	config = &MiddlewareConfig{RecoverableShell: &recoverableShellStub{}}
	require.ErrorContains(t, config.Validate(), "requires a background Manager")
}
