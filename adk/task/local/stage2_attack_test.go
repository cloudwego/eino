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

package local

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
	"github.com/cloudwego/eino/schema"
)

type attackPublishErrorStore struct {
	*background.InMemoryStore
	err error
}

func (s *attackPublishErrorStore) Publish(
	context.Context,
	*background.PublishTaskRequest,
) (*background.TaskSnapshot, error) {
	return nil, s.err
}

// TestAttack_StreamConstructorCancellation verifies that caller cancellation
// releases RunStream even when construction is uncooperative. Waiting for the
// constructor would leak the foreground call and its mailbox ownership.
func TestAttack_StreamConstructorCancellation(t *testing.T) {
	timeout := 0
	runner, manager := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
	})
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct {
		stream *schema.StreamReader[string]
		err    error
	}, 1)

	go func() {
		stream, err := runner.RunStream(
			ctx,
			&Input{Description: "blocked constructor"},
			func(
				context.Context,
				background.ExecutionRuntime,
			) (*schema.StreamReader[string], error) {
				close(entered)
				<-release
				return streamWork("late")(context.Background(), nil)
			},
		)
		returned <- struct {
			stream *schema.StreamReader[string]
			err    error
		}{stream: stream, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stream constructor did not start")
	}
	select {
	case result := <-returned:
		t.Fatalf("RunStream returned before cancellation: %#v", result)
	default:
	}
	cancel()
	select {
	case result := <-returned:
		require.Nil(t, result.stream)
		require.ErrorIs(t, result.err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("RunStream remained blocked after caller cancellation")
	}
	waitMailboxState(t, manager, "test_1", task.MailboxSealed)
	close(release)
}

// TestAttack_StreamTimeoutStartsAfterReady verifies that constructor latency
// does not consume the foreground projection budget. Starting the timer before
// readiness would discard a valid stream immediately after construction.
func TestAttack_StreamTimeoutStartsAfterReady(t *testing.T) {
	timeout := 20
	runner, manager := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct {
		stream *schema.StreamReader[string]
		err    error
	}, 1)

	go func() {
		stream, err := runner.RunStream(
			context.Background(),
			&Input{Description: "slow constructor"},
			func(
				context.Context,
				background.ExecutionRuntime,
			) (*schema.StreamReader[string], error) {
				close(entered)
				<-release
				return streamWork("ready")(context.Background(), nil)
			},
		)
		returned <- struct {
			stream *schema.StreamReader[string]
			err    error
		}{stream: stream, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stream constructor did not start")
	}
	time.Sleep(2 * time.Duration(timeout) * time.Millisecond)
	select {
	case result := <-returned:
		t.Fatalf("foreground timeout fired before stream readiness: %#v", result)
	default:
	}
	close(release)
	result := <-returned
	require.NoError(t, result.err)
	require.Equal(t, "ready", drain(t, result.stream))
	waitMailboxState(t, manager, "test_1", task.MailboxSealed)
}

// TestAttack_ForegroundMailboxPendingInputSurvivesCompletion verifies that an
// idle-seal failure does not erase pending input. Losing that input would make
// a successful foreground result silently destroy the next turn.
func TestAttack_ForegroundMailboxPendingInputSurvivesCompletion(t *testing.T) {
	runner, manager := newTestRunner(t)
	result, err := runner.Run(
		context.Background(),
		&Input{Description: "pending input"},
		func(ctx context.Context, _ background.ExecutionRuntime) (string, error) {
			execution, ok := task.ExecutionContextFromContext(ctx)
			if !ok {
				return "", errors.New("execution context is missing")
			}
			_, sendErr := manager.SendInput(
				context.Background(),
				&task.SendInputRequest{
					TaskID: execution.TaskID,
					Input: task.Input{
						EventID: "attack-pending",
						Kind:    "message",
						Data:    []byte("preserve"),
					},
				},
			)
			return "done", sendErr
		},
	)

	require.ErrorIs(t, err, task.ErrInputsPending)
	outcome := requireForegroundResult(t, result)
	require.Equal(t, task.OutcomeCompleted, outcome.Status)
	mailbox, mailboxErr := manager.GetMailbox(context.Background(), result.ID())
	require.NoError(t, mailboxErr)
	require.Equal(t, task.MailboxForeground, mailbox.State)
	require.Equal(t, int64(1), mailbox.LatestSequence)
	require.Zero(t, mailbox.ConsumedCursor)
	inputs, listErr := manager.ListInputs(
		context.Background(),
		&task.ListInputsRequest{
			TaskID: result.ID(), AfterSequence: 0, Limit: 10,
		},
	)
	require.NoError(t, listErr)
	require.Len(t, inputs.Inputs, 1)
	require.Equal(t, "preserve", string(inputs.Inputs[0].Data))
}

// TestAttack_PublishFailureCleansLocalWork verifies that a failed detach
// publication cancels the task and removes its process-local closure. Retaining
// either would leave hidden work running or leak an unreachable closure.
func TestAttack_PublishFailureCleansLocalWork(t *testing.T) {
	publishErr := errors.New("attack publish failed")
	store := &attackPublishErrorStore{
		InMemoryStore: background.NewInMemoryStore(nil),
		err:           publishErr,
	}
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&background.Config{
			Tasks:      store,
			TaskEvents: store,
			IDGen: func(
				context.Context,
				*background.AllocateTaskIDRequest,
			) (string, error) {
				return "test_1", nil
			},
		},
	)
	timeout := 1
	runner, err := New(&Config{
		Manager:             manager,
		ForegroundTimeoutMs: &timeout,
		ShouldAutoBackground: func(
			context.Context,
			*foreground.CandidateInfo,
		) bool {
			return true
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, manager.Close(closeCtx))
	})
	workCanceled := make(chan struct{})

	result, err := runner.Run(
		context.Background(),
		&Input{Description: "publish failure"},
		func(ctx context.Context, _ background.ExecutionRuntime) (string, error) {
			<-ctx.Done()
			close(workCanceled)
			return "", ctx.Err()
		},
	)
	require.Nil(t, result)
	require.ErrorIs(t, err, publishErr)
	select {
	case <-workCanceled:
	case <-time.After(time.Second):
		t.Fatal("publication failure did not cancel local work")
	}
	snapshot, getErr := manager.Get(context.Background(), "test_1")
	require.NoError(t, getErr)
	require.Equal(t, background.StatusCanceled, snapshot.Status)
	require.Equal(t, background.PublicationDeferred, snapshot.Publication)
	require.Equal(t, "foreground projection publication failed", snapshot.ResultError)
	_, resolveErr := runner.executor.resolve(background.Spec{ID: "test_1"})
	require.Error(t, resolveErr)
}

// TestAttack_BufferedAndStreamTaskOwnershipParity verifies that buffered and
// streaming fast paths agree on durable ownership, publication, and terminal
// output. Divergence would make ownership depend on the transport shape.
func TestAttack_BufferedAndStreamTaskOwnershipParity(t *testing.T) {
	timeout := 100
	runner, manager := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
		config.ShouldAutoBackground = func(
			context.Context,
			*foreground.CandidateInfo,
		) bool {
			return true
		}
	})

	bufferedResult, err := runner.Run(
		context.Background(),
		&Input{Description: "buffered"},
		func(context.Context, background.ExecutionRuntime) (string, error) {
			return "ab", nil
		},
	)
	require.NoError(t, err)
	bufferedTask := requireTaskResult(t, bufferedResult)
	require.Equal(t, background.StatusCompleted, bufferedTask.Status)
	storedBuffered, err := manager.Get(context.Background(), bufferedResult.ID())
	require.NoError(t, err)
	require.Equal(t, background.PublicationDeferred, storedBuffered.Publication)

	stream, err := runner.RunStream(
		context.Background(),
		&Input{Description: "stream"},
		streamWork("a", "b"),
	)
	require.NoError(t, err)
	require.Equal(t, "ab", drain(t, stream))
	streamTask, err := manager.Get(context.Background(), "test_2")
	require.NoError(t, err)
	streamTask = waitTerminal(t, manager, streamTask)

	require.Equal(t, bufferedTask.Status, streamTask.Status)
	require.Equal(t, bufferedTask.Publication, streamTask.Publication)
	require.Equal(t, background.PublicationDeferred, streamTask.Publication)
	require.Equal(t, bufferedTask.ResultData, streamTask.ResultData)
	require.Equal(t, []byte("ab"), streamTask.ResultData)
}
