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

package task_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
)

func TestInputSeparatesSendIntentFromPersistedRecord(t *testing.T) {
	inputType := reflect.TypeOf(task.Input{})
	for _, field := range []string{"EventID", "Kind", "Data", "Delivery"} {
		_, ok := inputType.FieldByName(field)
		require.True(t, ok, "Input.%s must exist", field)
	}
	for _, field := range []string{"TaskID", "Sequence", "CreatedAt"} {
		_, ok := inputType.FieldByName(field)
		require.False(t, ok, "Input.%s belongs to InputRecord", field)
	}

	recordType := reflect.TypeOf(task.InputRecord{})
	for _, field := range []string{"TaskID", "Sequence", "Input", "CreatedAt"} {
		_, ok := recordType.FieldByName(field)
		require.True(t, ok, "InputRecord.%s must exist", field)
	}
}

func TestClientSendInput(t *testing.T) {
	ctx := context.Background()
	store := background.NewInMemoryStore(nil)
	registered, err := store.Register(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "task", InvocationID: "call",
	})
	require.NoError(t, err)
	client := &task.InputClient{Sender: store}
	result, err := client.SendInput(ctx, registered.Mailbox.TaskID, &task.Input{
		EventID: "event", Kind: "message", Data: []byte("payload"),
		Delivery: task.InputPreempt,
	})
	require.NoError(t, err)
	require.True(t, result.Inserted)
	require.Equal(t, task.InputPreempt, result.Input.Delivery)
	require.Equal(t, []byte("payload"), result.Input.Data)
	_, err = client.SendInput(ctx, registered.Mailbox.TaskID, nil)
	require.ErrorIs(t, err, task.ErrInputRequired)
	var nilClient *task.InputClient
	_, err = nilClient.SendInput(ctx, registered.Mailbox.TaskID, &task.Input{})
	require.ErrorIs(t, err, task.ErrMailboxStoreRequired)
}

func TestExecutionContextRoundTrip(t *testing.T) {
	execution := task.ExecutionContext{
		TaskID: "task-1", Owner: task.OwnerManager,
		Generation: 2, Attempt: 3, RootSessionID: "root",
	}
	ctx := task.WithExecutionContext(context.Background(), execution)
	actual, ok := task.ExecutionContextFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, execution, actual)
	_, ok = task.ExecutionContextFromContext(context.Background())
	require.False(t, ok)
}
