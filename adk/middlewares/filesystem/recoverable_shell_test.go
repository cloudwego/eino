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
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	backgroundlocal "github.com/cloudwego/eino/adk/backgroundtask/local"
	backgroundshell "github.com/cloudwego/eino/adk/backgroundtask/shell"
	backgroundtool "github.com/cloudwego/eino/adk/backgroundtask/tool"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type recoverableShellStub struct {
	startRequest   *backgroundshell.StartCommandRequest
	recoverRequest *backgroundshell.RecoverCommandRequest
}

func TestBackgroundConfigSeparatesExecutionModes_BitsUT(t *testing.T) {
	configType := reflect.TypeOf(BackgroundConfig{})
	for _, field := range []string{"Local", "Recoverable", "NotificationSessionID"} {
		_, ok := configType.FieldByName(field)
		require.True(t, ok)
	}
	for _, field := range []string{"Runner", "Manager", "Executors", "OutputStore"} {
		_, ok := configType.FieldByName(field)
		require.False(t, ok)
	}
	_, legacyHasBackground := reflect.TypeOf(Config{}).FieldByName("Background")
	require.False(t, legacyHasBackground)
	middlewareType := reflect.TypeOf(MiddlewareConfig{})
	_, hasRecoverableShell := middlewareType.FieldByName("RecoverableShell")
	require.False(t, hasRecoverableShell)
}

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
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return "shell-task", nil
		},
	})
	config := &MiddlewareConfig{
		Background: &BackgroundConfig{
			Recoverable: &RecoverableBackgroundConfig{
				Shell: shell, Manager: manager, Executors: executors,
			},
		},
		notificationSessionID: func(context.Context) (string, error) {
			return "session", nil
		},
	}
	tools, err := getFilesystemTools(context.Background(), config)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	result, err := tools[0].(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), &schema.ToolArgument{Text: `{"command":"echo hello"}`},
	)
	require.NoError(t, err)
	require.NotEmpty(t, result.Parts)
	var event backgroundtool.ManagedToolResponseEvent
	require.NoError(t, json.Unmarshal([]byte(result.Parts[0].Text), &event))
	require.Equal(t, backgroundtool.ManagedToolResponseEventForegroundResult, event.Type)
	require.Empty(t, event.TaskID)
	require.Equal(t, backgroundtask.StatusCompleted, event.Status)
	require.Equal(t, "command output", event.Output)
	require.NotNil(t, shell.startRequest)
	require.Equal(t, "shell-task", shell.startRequest.TaskID)
	require.Equal(t, int64(0), shell.startRequest.Attempt)
	require.Equal(t, "echo hello", shell.startRequest.Command)
}

func TestRecoverableShellConfigurationIsExclusive(t *testing.T) {
	config := &MiddlewareConfig{
		Shell: &mockShellBackend{},
		Background: &BackgroundConfig{
			Recoverable: &RecoverableBackgroundConfig{
				Shell:     &recoverableShellStub{},
				Manager:   mustNewBackgroundManager(t, context.Background(), nil),
				Executors: backgroundtask.NewExecutorRegistry(),
			},
		},
	}
	require.ErrorContains(t, config.Validate(), "mutually exclusive")

	config = &MiddlewareConfig{Background: &BackgroundConfig{
		Recoverable: &RecoverableBackgroundConfig{Shell: &recoverableShellStub{}},
	}}
	require.ErrorContains(t, config.Validate(), "Shell, Manager, and Executors are required")

	managerOne := mustNewBackgroundManager(t, context.Background(), nil)
	runner, err := backgroundlocal.New(&backgroundlocal.Config{Manager: managerOne, Executors: backgroundtask.NewExecutorRegistry()})
	require.NoError(t, err)
	config = &MiddlewareConfig{
		Shell: &mockShellBackend{},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: runner},
			Recoverable: &RecoverableBackgroundConfig{
				Shell: &recoverableShellStub{}, Manager: managerOne,
				Executors: backgroundtask.NewExecutorRegistry(),
			},
		},
	}
	require.ErrorContains(t, config.Validate(), "exactly one")
}

func TestRecoverableShellConstructor(t *testing.T) {
	manager := mustNewBackgroundManager(t, context.Background(), nil)
	middleware, err := New(context.Background(), &MiddlewareConfig{
		Background: &BackgroundConfig{Recoverable: &RecoverableBackgroundConfig{
			Shell: &recoverableShellStub{}, Manager: manager,
			Executors: backgroundtask.NewExecutorRegistry(),
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, middleware)
}
