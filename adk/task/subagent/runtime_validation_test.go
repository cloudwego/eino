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

package subagent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

func TestRuntimeCheckpointValidation(t *testing.T) {
	final := schema.AssistantMessage("done", nil)
	data, err := encodeRuntimeCheckpoint[*schema.Message](3, final)
	require.NoError(t, err)
	checkpoint, err := decodeRuntimeCheckpoint[*schema.Message](data)
	require.NoError(t, err)
	require.Equal(t, int64(3), checkpoint.InputCursor)
	require.Equal(t, "done", checkpoint.Final.Content)

	for _, value := range []turnLoopCheckpoint{
		{Version: 99, Mode: runtimeCheckpointIdle},
		{Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle, InputCursor: -1},
		{
			Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle,
			TargetIDs: []string{"target"},
		},
		{Version: runtimeCheckpointVersion, Mode: runtimeCheckpointResume},
		{Version: runtimeCheckpointVersion, Mode: "unknown"},
	} {
		encoded, marshalErr := json.Marshal(value)
		require.NoError(t, marshalErr)
		_, decodeErr := decodeRuntimeCheckpoint[*schema.Message](encoded)
		require.Error(t, decodeErr)
	}
	_, err = decodeRuntimeResumeTargets([]byte(`{}`))
	require.Error(t, err)
	_, err = decodeRuntimeResumeTargets([]byte(`{"target":1} trailing`))
	require.Error(t, err)
	targets, err := decodeRuntimeResumeTargets([]byte(`{"target":9007199254740993}`))
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), targets["target"])
}

func TestCompletionActionZeroValueFailsClosed(t *testing.T) {
	require.Equal(t, CompletionUnknown, CompletionAction(0))
	require.ErrorContains(
		t,
		validateCompletionAction(CompletionAction(0)),
		"invalid completion action",
	)
}

func TestSignalsToInputMergesMessagesAndExternalEvents(t *testing.T) {
	runtime, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		func(
			_ context.Context,
			inputs []*task.InputRecord,
		) (*adk.AgentInput, error) {
			require.Len(t, inputs, 1)
			require.Equal(t, "external", inputs[0].Kind)
			return &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("external")},
			}, nil
		},
	)
	encoded, err := encodeTypedInput(&adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("initial")},
	})
	require.NoError(t, err)
	data, err := json.Marshal(encoded)
	require.NoError(t, err)
	input, err := runtime.signalsToInput(context.Background(), []*task.InputRecord{
		{Input: task.Input{EventID: "initial", Kind: initialSignalKind, Data: data}},
		{Input: task.Input{EventID: "resume", Kind: ResumeInputKind, Data: []byte(`{"target":true}`)}},
		{Input: task.Input{EventID: "external", Kind: "external", Data: []byte("event")}},
	})
	require.NoError(t, err)
	require.Len(t, input.Messages, 2)
	require.Equal(t, "initial", input.Messages[0].Content)
	require.Equal(t, "external", input.Messages[1].Content)
	stampRuntimeInputIDs(input, []*task.InputRecord{{
		Input: task.Input{EventID: "batch"},
	}})
	require.NotEmpty(t, input.Messages[0].Extra)
	require.NotEmpty(t, input.Messages[1].Extra)

	_, err = runtime.signalsToInput(context.Background(), []*task.InputRecord{{
		Input: task.Input{
			EventID: "broken", Kind: initialSignalKind, Data: []byte("{"),
		},
	}})
	require.Error(t, err)
}

func TestRuntimeMetadataAndForegroundResultValidation(t *testing.T) {
	valid, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "root", ChildSessionID: "child", AgentName: "worker",
	})
	require.NoError(t, err)
	metadata, err := decodeRuntimeMetadata(valid)
	require.NoError(t, err)
	require.Equal(t, "root", metadata.RootSessionID)

	_, err = decodeRuntimeMetadata([]byte(`{"version":1}`))
	require.Error(t, err)
	_, err = decodeForegroundResultCheckpoint([]byte(`{"version":1}`))
	require.Error(t, err)
	_, err = decodeForegroundResultCheckpoint([]byte(
		`{"version":1,"final_message":{},"error":"both"}`,
	))
	require.Error(t, err)
}

func TestExecutorProtocolDoesNotClaimLegacyV4Tasks(t *testing.T) {
	const legacyExecutorKey = "eino.dev/subagent"

	require.Equal(t, "eino.dev/task-subagent", ExecutorKey)
	require.Equal(t, 1, payloadVersion)

	legacySpec := background.Spec{
		ID: "legacy-v4", ExecutorKey: legacyExecutorKey, Kind: "subagent",
		Payload: []byte(
			`{"version":4,"subagent_name":"worker","input":{"messages":[]},"child_session_id":"child"}`,
		),
	}
	executor := newExecutor[*schema.Message](nil)
	require.ErrorContains(t, executor.ValidateSpec(legacySpec), "invalid executor key")

	store := background.NewInMemoryStore(nil)
	created, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: legacySpec, LeaseExpiryPolicy: background.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	manager, err := background.New(context.Background(), &background.Config{
		Tasks: store, TaskEvents: store,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, manager.Close(context.Background()))
	})
	_, loaded, err := manager.LoadOrRegisterExecutor(executor)
	require.NoError(t, err)
	require.False(t, loaded)

	err = manager.Execute(context.Background(), created.Spec.ID)
	require.ErrorContains(t, err, `executor "eino.dev/subagent" is unavailable`)
	current, err := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, current.Status)
	require.Zero(t, current.Attempt)
}

func TestExecutorPayloadValidation(t *testing.T) {
	executor := newExecutor[*schema.Message](nil)
	require.Error(t, executor.ValidateExecution(context.Background(), nil))
	require.Error(t, executor.ValidateSpec(background.Spec{}))

	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, SubAgentName: "worker", ChildSessionID: "child",
	})
	require.NoError(t, err)
	spec := background.Spec{
		ID: "task", ExecutorKey: ExecutorKey, Kind: "subagent", Payload: payload,
	}
	require.ErrorContains(t, executor.ValidateSpec(spec), "controller is unavailable")

	runtime, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	require.NotNil(t, runtime)
	registered := runtime.executor
	require.NoError(t, registered.ValidateSpec(spec))
	require.NoError(t, registered.ValidateExecution(
		context.Background(), &background.TaskSnapshot{Spec: spec},
	))

	var nilController *Controller[*schema.Message]
	require.Error(t, nilController.RegisterAgent("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	require.Error(t, runtime.RegisterAgent("", nil))
	require.ErrorIs(t, runtime.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{
			Agent: &resumableTestAgent{name: "worker"},
		},
	), background.ErrAlreadyExists)

	invalidVersion := spec
	invalidVersion.Payload = []byte(
		`{"version":99,"subagent_name":"worker","child_session_id":"child"}`,
	)
	require.ErrorIs(
		t,
		registered.ValidateSpec(invalidVersion),
		background.ErrUnsupportedExecutorPayloadVersion,
	)
}
