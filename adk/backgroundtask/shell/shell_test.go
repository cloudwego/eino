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

package shell

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	backgroundtool "github.com/cloudwego/eino/adk/backgroundtask/tool"
	"github.com/cloudwego/eino/schema"
)

type shellStub struct {
	start   *StartCommandRequest
	recover *RecoverCommandRequest
	run     backgroundtool.Run
}

func TestRecoverCommandRequestHasNoCheckpoint_BitsUT(t *testing.T) {
	_, exists := reflect.TypeOf(RecoverCommandRequest{}).FieldByName("Checkpoint")
	require.False(t, exists)
}

func (s *shellStub) StartCommand(
	_ context.Context,
	request *StartCommandRequest,
) (backgroundtool.Run, error) {
	s.start = request
	return s.run, nil
}
func (s *shellStub) RecoverCommand(
	_ context.Context,
	request *RecoverCommandRequest,
) (backgroundtool.Run, error) {
	s.recover = request
	return s.run, nil
}

type runStub struct{}

func (runStub) Wait(context.Context) (*backgroundtool.Outcome, error) { return nil, nil }
func (runStub) Stop(context.Context) error                            { return nil }

func TestNewRegistrationAndAdapter(t *testing.T) {
	for _, config := range []*RegistrationConfig{
		nil,
		{Info: &schema.ToolInfo{Name: "execute"}},
		{Shell: &shellStub{}},
	} {
		_, err := NewRegistration(config)
		require.Error(t, err)
	}

	backend := &shellStub{run: runStub{}}
	registration, err := NewRegistration(&RegistrationConfig{
		Info:  &schema.ToolInfo{Name: "execute"},
		Shell: backend,
	})
	require.NoError(t, err)
	require.Equal(t, "echo hello", registration.Description(`{"command":"echo hello"}`))
	require.Equal(t, "Run shell command", registration.Description(`{"command":`))

	adapted := registration.Tool.(backgroundtool.RecoverableBackgroundTool)
	require.NoError(t, adapted.ValidateArguments(`{"command":"echo hello"}`))
	require.Error(t, adapted.ValidateArguments(`{"command":""}`))
	require.Error(t, adapted.ValidateArguments(`{`))

	started, err := adapted.Start(context.Background(), &backgroundtool.StartRequest{
		TaskID: "task", Arguments: `{"command":"echo hello"}`, Attempt: 1,
	})
	require.NoError(t, err)
	require.Equal(t, backend.run, started.Run)
	require.Empty(t, started.Checkpoint)
	require.Equal(t, &StartCommandRequest{
		TaskID: "task", Command: "echo hello", Attempt: 1,
	}, backend.start)
	_, err = adapted.Start(context.Background(), nil)
	require.Error(t, err)
	_, err = adapted.Start(context.Background(), &backgroundtool.StartRequest{
		Arguments: `{`,
	})
	require.Error(t, err)

	recovered, err := adapted.Recover(context.Background(), &backgroundtool.RecoverRequest{
		TaskID: "task", Arguments: `{"command":"echo hello"}`, Attempt: 2,
	})
	require.NoError(t, err)
	require.Equal(t, backend.run, recovered)
	require.Equal(t, &RecoverCommandRequest{
		TaskID: "task", Command: "echo hello", Attempt: 2,
	}, backend.recover)
	_, err = adapted.Recover(context.Background(), nil)
	require.Error(t, err)
	_, err = adapted.Recover(context.Background(), &backgroundtool.RecoverRequest{
		Arguments: `{`,
	})
	require.Error(t, err)
}
