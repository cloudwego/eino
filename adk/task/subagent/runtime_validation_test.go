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
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	require.Equal(t, runtimeCheckpointVersion, checkpoint.Version)
	require.Equal(t, int64(3), checkpoint.InputCursor)
	require.Equal(t, "done", checkpoint.Final.Content)
	require.Empty(t, checkpoint.SparseAcks)
	require.Empty(t, checkpoint.TurnLoopCheckpoint)
	require.Error(t, decodeLegacyRuntimeCheckpointForTest(data))
	require.True(t, len(data) > len(runtimeCheckpointMagic))
	require.Equal(t, runtimeCheckpointMagic, string(data[:len(runtimeCheckpointMagic)]))
	require.Equal(t, byte(runtimeCheckpointVersion), data[len(runtimeCheckpointMagic)])

	data, err = encodeRuntimeCheckpointState[*schema.Message](
		1, []int64{3}, final, []byte("opaque turn loop state"),
	)
	require.NoError(t, err)
	checkpoint, err = decodeRuntimeCheckpoint[*schema.Message](data)
	require.NoError(t, err)
	require.Equal(t, int64(1), checkpoint.InputCursor)
	require.Equal(t, []int64{3}, checkpoint.SparseAcks)
	require.Equal(t, []byte("opaque turn loop state"), checkpoint.TurnLoopCheckpoint)

	legacyFinal, err := encodeRuntimeMessage[*schema.Message](final)
	require.NoError(t, err)
	legacyData, err := json.Marshal(&turnLoopCheckpoint{
		Version: legacyRuntimeCheckpointVersion, Mode: runtimeCheckpointIdle,
		InputCursor: 2, FinalMessage: legacyFinal,
	})
	require.NoError(t, err)
	legacy, err := decodeRuntimeCheckpoint[*schema.Message](legacyData)
	require.NoError(t, err)
	require.Equal(t, legacyRuntimeCheckpointVersion, legacy.Version)
	require.Equal(t, int64(2), legacy.InputCursor)
	require.Equal(t, "done", legacy.Final.Content)
	require.Empty(t, legacy.TurnLoopCheckpoint)
	require.NoError(t, decodeLegacyRuntimeCheckpointForTest(legacyData))

	for _, value := range []turnLoopCheckpoint{
		{Version: 99, Mode: runtimeCheckpointIdle},
		{Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle, InputCursor: -1},
		{
			Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle,
			TargetIDs: []string{"target"},
		},
		{Version: runtimeCheckpointVersion, Mode: runtimeCheckpointResume},
		{
			Version: runtimeCheckpointVersion, Mode: runtimeCheckpointResume,
			TargetIDs: []string{"target"},
		},
		{Version: runtimeCheckpointVersion, Mode: "unknown"},
		{
			Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle,
			InputCursor: 1, SparseAcks: []int64{3, 3},
		},
		{
			Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle,
			InputCursor: 1, SparseAcks: []int64{2},
		},
		{
			Version: legacyRuntimeCheckpointVersion, Mode: runtimeCheckpointIdle,
			SparseAcks: []int64{2},
		},
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

func TestValidateRuntimeCheckpoint(t *testing.T) {
	tests := []struct {
		name       string
		checkpoint *turnLoopCheckpoint
		wantErr    string
	}{
		{
			name: "negative cursor",
			checkpoint: &turnLoopCheckpoint{
				Version:     runtimeCheckpointVersion,
				Mode:        runtimeCheckpointIdle,
				InputCursor: -1,
			},
			wantErr: "task/subagent: incompatible runtime checkpoint",
		},
		{
			name: "idle checkpoint with resume targets",
			checkpoint: &turnLoopCheckpoint{
				Version:   runtimeCheckpointVersion,
				Mode:      runtimeCheckpointIdle,
				TargetIDs: []string{"target"},
			},
			wantErr: "task/subagent: idle runtime checkpoint contains resume targets",
		},
		{
			name: "resume checkpoint without targets",
			checkpoint: &turnLoopCheckpoint{
				Version:            runtimeCheckpointVersion,
				Mode:               runtimeCheckpointResume,
				TurnLoopCheckpoint: []byte("turn loop state"),
			},
			wantErr: "task/subagent: interrupt runtime checkpoint has no targets",
		},
		{
			name: "invalid mode",
			checkpoint: &turnLoopCheckpoint{
				Version: runtimeCheckpointVersion,
				Mode:    "invalid",
			},
			wantErr: "task/subagent: runtime checkpoint mode is invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.EqualError(
				t,
				validateRuntimeCheckpoint(test.checkpoint),
				test.wantErr,
			)
		})
	}
}

func TestRuntimeCheckpointLargeOpaqueStateFitsLifecycleLimit(t *testing.T) {
	const lifecycleLimit = 1 << 20
	runnerState := make([]byte, lifecycleLimit-256)
	_, err := rand.Read(runnerState)
	require.NoError(t, err)

	encoded, err := encodeRuntimeCheckpointState[*schema.Message](
		17,
		[]int64{19, 21},
		schema.AssistantMessage("partial", nil),
		runnerState,
	)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), lifecycleLimit)
	require.Greater(t, base64.StdEncoding.EncodedLen(len(runnerState)), lifecycleLimit)

	decoded, err := decodeRuntimeCheckpoint[*schema.Message](encoded)
	require.NoError(t, err)
	require.Equal(t, int64(17), decoded.InputCursor)
	require.Equal(t, []int64{19, 21}, decoded.SparseAcks)
	require.Equal(t, "partial", decoded.Final.Content)
	require.Equal(t, runnerState, decoded.TurnLoopCheckpoint)

	store := background.NewInMemoryStore(nil)
	created, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: background.Spec{
			ID: "large-runtime-checkpoint", ExecutorKey: "test",
		},
		LeaseExpiryPolicy: background.LeaseExpiryRetry,
		Checkpoint:        encoded,
	})
	require.NoError(t, err)
	require.Equal(t, encoded, created.Checkpoint)
}

func TestRuntimeCheckpointCodecRejectsMalformedData(t *testing.T) {
	validMetadata := []byte{
		runtimeCheckpointModeIdle,
		0, // cursor
		0, // sparse ack count
		0, // target count
	}
	envelope := func(metadata, final, runner []byte) []byte {
		data := append([]byte(runtimeCheckpointMagic), byte(runtimeCheckpointVersion))
		data = appendRuntimeBytes(data, metadata)
		data = appendRuntimeBytes(data, final)
		return appendRuntimeBytes(data, runner)
	}
	overflow := append(
		append([]byte(runtimeCheckpointMagic), byte(runtimeCheckpointVersion)),
		[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x02}...,
	)
	invalidSparseCount := []byte{runtimeCheckpointModeIdle, 0}
	invalidSparseCount = appendRuntimeUvarint(
		invalidSparseCount,
		maxSparseAcks+1,
	)
	overflowingCursor := []byte{runtimeCheckpointModeIdle}
	overflowingCursor = appendRuntimeUvarint(overflowingCursor, ^uint64(0))
	overflowingCursor = append(overflowingCursor, 0, 0)
	resumeWithoutState := []byte{
		runtimeCheckpointModeResume,
		0, // cursor
		0, // sparse ack count
		1, // target count
		1, 'x',
	}
	oldV2JSON, err := json.Marshal(&turnLoopCheckpoint{
		Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle,
	})
	require.NoError(t, err)

	for _, testCase := range []struct {
		name string
		data []byte
	}{
		{name: "magic only", data: []byte(runtimeCheckpointMagic)},
		{
			name: "unsupported version",
			data: append([]byte(runtimeCheckpointMagic), 99),
		},
		{name: "overflowing section length", data: overflow},
		{
			name: "truncated section",
			data: append(
				append([]byte(runtimeCheckpointMagic), byte(runtimeCheckpointVersion)),
				5, 0,
			),
		},
		{name: "empty metadata", data: envelope(nil, nil, nil)},
		{
			name: "invalid mode",
			data: envelope([]byte{99, 0, 0, 0}, nil, nil),
		},
		{
			name: "cursor exceeds int64",
			data: envelope(overflowingCursor, nil, nil),
		},
		{
			name: "truncated sparse ack",
			data: envelope([]byte{runtimeCheckpointModeIdle, 0, 1}, nil, nil),
		},
		{
			name: "sparse ack count exceeds limit",
			data: envelope(invalidSparseCount, nil, nil),
		},
		{
			name: "target count exceeds metadata",
			data: envelope([]byte{runtimeCheckpointModeIdle, 0, 0, 1}, nil, nil),
		},
		{
			name: "metadata trailing data",
			data: envelope(append(validMetadata, 0), nil, nil),
		},
		{
			name: "resume without runner state",
			data: envelope(resumeWithoutState, nil, nil),
		},
		{
			name: "envelope trailing data",
			data: append(envelope(validMetadata, nil, nil), 0),
		},
		{name: "old unpublished v2 JSON", data: oldV2JSON},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, decodeErr := decodeRuntimeCheckpoint[*schema.Message](testCase.data)
			require.Error(t, decodeErr)
		})
	}

	valid, err := encodeRuntimeCheckpoint[*schema.Message](0, nil)
	require.NoError(t, err)
	for size := len(runtimeCheckpointMagic) + 1; size < len(valid); size++ {
		_, decodeErr := decodeRuntimeCheckpoint[*schema.Message](valid[:size])
		require.Error(t, decodeErr, "accepted truncation at byte %d", size)
	}
}

func decodeLegacyRuntimeCheckpointForTest(data []byte) error {
	var checkpoint struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return err
	}
	if checkpoint.Version != legacyRuntimeCheckpointVersion {
		return errors.New("legacy worker rejected runtime checkpoint")
	}
	return nil
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

	final, err := encodeRuntimeMessage(schema.AssistantMessage("legacy", nil))
	require.NoError(t, err)
	legacy, err := json.Marshal(&foregroundResultCheckpoint{
		Version:      legacyForegroundResultVersion,
		Status:       task.OutcomeCompleted,
		FinalMessage: final,
	})
	require.NoError(t, err)
	checkpoint, err := decodeForegroundResultCheckpoint(legacy)
	require.NoError(t, err)
	require.Equal(t, legacyForegroundResultVersion, checkpoint.Version)
	require.Equal(t, task.OutcomeCompleted, checkpoint.Status)

	marker, err := json.Marshal(&foregroundResultCheckpoint{
		Version: foregroundResultVersion,
		State:   foregroundResultInvalidated,
	})
	require.NoError(t, err)
	checkpoint, err = decodeForegroundResultCheckpoint(marker)
	require.NoError(t, err)
	require.Equal(t, foregroundResultInvalidated, checkpoint.State)

	for _, malformed := range [][]byte{
		[]byte(`{"version":2,"state":"invalidated","status":1}`),
		[]byte(`{"version":2,"state":"invalidated","unknown":true}`),
		[]byte(`{"version":2,"state":"terminal"}`),
		[]byte(`{"version":2,"state":"invalidated"} {}`),
	} {
		_, err = decodeForegroundResultCheckpoint(malformed)
		require.Error(t, err)
	}
}

func TestExecutorProtocolDoesNotClaimLegacyV4Tasks(t *testing.T) {
	const (
		legacyV4ExecutorKey      = "eino.dev/subagent"
		legacyDurableExecutorKey = "eino.dev/task-subagent"
	)

	require.Equal(t, "eino.dev/task-subagent-durable-v2", ExecutorKey)
	require.Equal(t, 1, payloadVersion)

	legacySpec := background.Spec{
		ID: "legacy-v4", ExecutorKey: legacyV4ExecutorKey, Kind: "subagent",
		Payload: []byte(
			`{"version":4,"subagent_name":"worker","input":{"messages":[]},"child_session_id":"child"}`,
		),
	}
	executor := newExecutor[*schema.Message](nil)
	require.ErrorContains(t, executor.ValidateSpec(legacySpec), "invalid executor key")
	legacyDurableSpec := legacySpec
	legacyDurableSpec.ID = "legacy-durable-v1"
	legacyDurableSpec.ExecutorKey = legacyDurableExecutorKey
	legacyDurableSpec.Payload = []byte(
		`{"version":1,"subagent_name":"worker","child_session_id":"child"}`,
	)
	require.ErrorContains(
		t,
		executor.ValidateSpec(legacyDurableSpec),
		"invalid executor key",
	)

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
	require.ErrorContains(
		t,
		err,
		`executor "eino.dev/subagent" is unavailable`,
	)
	current, err := manager.Get(context.Background(), created.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, current.Status)
	require.Zero(t, current.Attempt)

	v2Task, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: background.Spec{
			ID: "durable-v2", ExecutorKey: ExecutorKey, Kind: "subagent",
			Payload: legacyDurableSpec.Payload,
		},
		LeaseExpiryPolicy: background.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	legacyPending, err := store.ListPending(
		context.Background(),
		&background.ListPendingRequest{
			ExecutorKeys: []string{legacyDurableExecutorKey}, Limit: 10,
		},
	)
	require.NoError(t, err)
	require.Empty(t, legacyPending.Tasks)
	require.Equal(t, ExecutorKey, v2Task.Spec.ExecutorKey)
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
