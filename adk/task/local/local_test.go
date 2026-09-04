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
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/internal/taskfirst"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *background.Config,
) *background.Manager {
	t.Helper()
	if config == nil {
		config = &background.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *background.TaskSnapshot) error { return nil }
	}
	manager, err := background.New(ctx, config)
	require.NoError(t, err)
	return manager
}

func awaitLocalTestValue[T any](
	t *testing.T,
	values <-chan T,
	description string,
) T {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatalf("%s channel closed before producing a value", description)
		}
		return value
	case <-timer.C:
		t.Fatalf("timed out after 1 second waiting for %s", description)
		var zero T
		return zero
	}
}

type countingGetStore struct {
	*background.InMemoryStore
	getCount int64
}

type mailboxFinalizationErrorStore struct {
	*background.InMemoryStore
	sealErr    error
	abandonErr error
}

type waitVersionErrorStore struct {
	*background.InMemoryStore
	err error
}

func (s *waitVersionErrorStore) WaitForTaskVersion(
	context.Context,
	*background.WaitForTaskVersionRequest,
) (*background.TaskSnapshot, error) {
	return nil, s.err
}

func (s *mailboxFinalizationErrorStore) SealIfIdle(
	context.Context,
	*task.SealMailboxRequest,
) (*task.Mailbox, error) {
	return nil, s.sealErr
}

func (s *mailboxFinalizationErrorStore) Abandon(
	context.Context,
	*task.AbandonMailboxRequest,
) (*task.Mailbox, error) {
	return nil, s.abandonErr
}

func (s *countingGetStore) Get(
	ctx context.Context,
	taskID string,
) (*background.TaskSnapshot, error) {
	atomic.AddInt64(&s.getCount, 1)
	return s.InMemoryStore.Get(ctx, taskID)
}

func TestInputUsesKindVocabulary_BitsUT(t *testing.T) {
	inputType := reflect.TypeOf(Input{})
	_, hasKind := inputType.FieldByName("Kind")
	_, hasType := inputType.FieldByName("Type")
	require.True(t, hasKind)
	require.False(t, hasType)
}

func TestRunResultRejectsZeroAndInvalidCombinations(t *testing.T) {
	for _, result := range []*RunResult{
		nil,
		{},
		{
			id:         "both",
			foreground: &task.Outcome{Status: task.OutcomeCompleted},
			task: &background.TaskSnapshot{
				Spec: background.Spec{ID: "both"},
			},
		},
		{
			id:   "expected",
			task: &background.TaskSnapshot{Spec: background.Spec{ID: "other"}},
		},
	} {
		require.Empty(t, result.ID())
		_, ok := result.Foreground()
		require.False(t, ok)
		_, ok = result.Task()
		require.False(t, ok)
	}

	for _, testCase := range []struct {
		name    string
		create  func() (*RunResult, error)
		wantErr string
	}{
		{
			name: "foreground empty id",
			create: func() (*RunResult, error) {
				return newForegroundRunResult(
					"",
					&task.Outcome{Status: task.OutcomeCompleted},
				)
			},
			wantErr: "task/local: invalid foreground run result",
		},
		{
			name: "foreground nil outcome",
			create: func() (*RunResult, error) {
				return newForegroundRunResult("foreground", nil)
			},
			wantErr: "task/local: invalid foreground run result",
		},
		{
			name: "foreground unknown status",
			create: func() (*RunResult, error) {
				return newForegroundRunResult(
					"foreground",
					&task.Outcome{Status: task.OutcomeUnknown},
				)
			},
			wantErr: "task/local: invalid foreground run result",
		},
		{
			name: "durable nil snapshot",
			create: func() (*RunResult, error) {
				return newTaskRunResult(nil)
			},
			wantErr: "task/local: invalid durable run result",
		},
		{
			name: "durable empty id",
			create: func() (*RunResult, error) {
				return newTaskRunResult(&background.TaskSnapshot{})
			},
			wantErr: "task/local: invalid durable run result",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := testCase.create()
			require.Nil(t, result)
			require.EqualError(t, err, testCase.wantErr)
		})
	}
}

func newTestRunner(t *testing.T, configure ...func(*Config)) (*Runner, *background.Manager) {
	t.Helper()
	var sequence int64
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return fmt.Sprintf("test_%d", atomic.AddInt64(&sequence, 1)), nil
		},
	})
	config := &Config{Manager: manager}
	for _, apply := range configure {
		apply(config)
	}
	runner, err := New(config)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	return runner, manager
}

func requireForegroundResult(t testing.TB, result *RunResult) *task.Outcome {
	t.Helper()
	require.NotNil(t, result)
	outcome, ok := result.Foreground()
	require.True(t, ok)
	require.NotNil(t, outcome)
	_, taskOK := result.Task()
	require.False(t, taskOK)
	require.NotEmpty(t, result.ID())
	return outcome
}

func requireTaskResult(t testing.TB, result *RunResult) *background.TaskSnapshot {
	t.Helper()
	require.NotNil(t, result)
	snapshot, ok := result.Task()
	require.True(t, ok)
	require.NotNil(t, snapshot)
	_, foregroundOK := result.Foreground()
	require.False(t, foregroundOK)
	require.Equal(t, snapshot.Spec.ID, result.ID())
	return snapshot
}

func waitTerminal(
	t *testing.T,
	manager *background.Manager,
	task *background.TaskSnapshot,
) *background.TaskSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for task.Status == background.StatusPending ||
		task.Status == background.StatusRunning {
		next, err := manager.WaitForTaskVersion(ctx, &background.WaitForTaskVersionRequest{
			TaskID: task.Spec.ID, AfterVersion: task.Version,
		})
		require.NoError(t, err)
		task = next
	}
	return task
}

func waitMailboxState(
	t *testing.T,
	manager *background.Manager,
	taskID string,
	want task.MailboxState,
) *task.Mailbox {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		mailbox, err := manager.GetMailbox(context.Background(), taskID)
		require.NoError(t, err)
		if mailbox.State == want {
			return mailbox
		}
		require.True(t, time.Now().Before(deadline), "mailbox remained %q", mailbox.State)
		time.Sleep(time.Millisecond)
	}
}

type prefixedLocalEventPersister struct{}

func (prefixedLocalEventPersister) Persist(
	ctx context.Context,
	_ background.TaskEventScope,
	input *background.TaskEventEnvelope[string, string],
	writer background.TaskEventWriter,
) error {
	_, err := writer.Append(ctx, &background.TaskEventPartInput{
		PartID: "custom", Data: []byte("encoded:" + input.Event),
		Final: true,
	})
	return err
}

func TestRunnerBufferedForegroundAndBackground_BitsUT(t *testing.T) {
	runner, manager := newTestRunner(t)
	foreground, err := runner.Run(context.Background(), &Input{Description: "foreground"},
		func(context.Context, background.ExecutionRuntime) (string, error) {
			return "done", nil
		},
	)
	require.NoError(t, err)
	foregroundOutcome := requireForegroundResult(t, foreground)
	assert.Equal(t, task.OutcomeCompleted, foregroundOutcome.Status)
	assert.Equal(t, "done", string(foregroundOutcome.Data))
	waitMailboxState(t, manager, foreground.ID(), task.MailboxSealed)
	_, err = manager.Get(context.Background(), foreground.ID())
	require.ErrorIs(t, err, background.ErrNotFound)

	executionContext := make(chan task.ExecutionContext, 1)
	backgroundResult, err := runner.Run(context.Background(), &Input{
		Description: "background", RunInBackground: true,
	}, func(ctx context.Context, _ background.ExecutionRuntime) (string, error) {
		current, _ := task.ExecutionContextFromContext(ctx)
		executionContext <- current
		time.Sleep(20 * time.Millisecond)
		return "later", nil
	})
	require.NoError(t, err)
	backgroundTask := requireTaskResult(t, backgroundResult)
	assert.Equal(t, background.StatusPending, backgroundTask.Status)
	current := <-executionContext
	require.Equal(t, task.OwnerManager, current.Owner)
	require.Equal(t, int64(1), current.Attempt)
	backgroundTask = waitTerminal(t, manager, backgroundTask)
	assert.Equal(t, background.StatusCompleted, backgroundTask.Status)
	assert.Equal(t, "later", string(backgroundTask.ResultData))
	require.NotEqual(t, foreground.ID(), backgroundResult.ID())
}

func TestRunnerForegroundTimeoutPolicies_BitsUT(t *testing.T) {
	t.Run("fail", func(t *testing.T) {
		timeout := 10
		runner, manager := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
		})
		result, err := runner.Run(context.Background(), &Input{Description: "timeout"},
			func(ctx context.Context, _ background.ExecutionRuntime) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
		)
		require.Nil(t, result)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var timeoutErr *task.ForegroundTimeoutError
		require.ErrorAs(t, err, &timeoutErr)
		assert.Equal(t, 10*time.Millisecond, timeoutErr.Timeout)
		assert.Equal(t, "test_1", timeoutErr.TaskID)
		waitMailboxState(t, manager, timeoutErr.TaskID, task.MailboxSealed)
		_, err = manager.Get(context.Background(), timeoutErr.TaskID)
		require.ErrorIs(t, err, background.ErrNotFound)
	})

	t.Run("auto background", func(t *testing.T) {
		timeout := 10
		runner, manager := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
			config.ShouldAutoBackground = func(context.Context, *foreground.CandidateInfo) bool {
				return true
			}
		})
		result, err := runner.Run(context.Background(), &Input{Description: "background"},
			func(context.Context, background.ExecutionRuntime) (string, error) {
				time.Sleep(30 * time.Millisecond)
				return "done", nil
			},
		)
		require.NoError(t, err)
		snapshot := requireTaskResult(t, result)
		assert.Equal(t, background.StatusRunning, snapshot.Status)
		snapshot = waitTerminal(t, manager, snapshot)
		assert.Equal(t, background.StatusCompleted, snapshot.Status)
	})

	t.Run("stream fail", func(t *testing.T) {
		timeout := 10
		runner, _ := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
		})
		stream, err := runner.RunStream(
			context.Background(),
			&Input{Description: "stream timeout"},
			func(ctx context.Context, _ background.ExecutionRuntime) (*schema.StreamReader[string], error) {
				reader, writer := schema.Pipe[string](1)
				go func() {
					<-ctx.Done()
					writer.Close()
				}()
				return reader, nil
			},
		)
		require.NoError(t, err)
		_, err = stream.Recv()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		var timeoutErr *task.ForegroundTimeoutError
		require.ErrorAs(t, err, &timeoutErr)
		assert.Equal(t, 10*time.Millisecond, timeoutErr.Timeout)
		assert.Equal(t, "test_1", timeoutErr.TaskID)
	})
}

func TestAttack_ForegroundTimeoutDoesNotMaskCallerDeadline(t *testing.T) {
	timeout := int(time.Second / time.Millisecond)
	runner, _ := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	release := make(chan struct{})

	result, err := runner.Run(ctx, &Input{Description: "caller deadline"},
		func(context.Context, background.ExecutionRuntime) (string, error) {
			<-release
			return "late", nil
		},
	)
	close(release)

	require.NoError(t, err)
	outcome := requireForegroundResult(t, result)
	require.Equal(t, task.OutcomeCanceled, outcome.Status)
	require.Equal(t, context.DeadlineExceeded.Error(), outcome.Error)
	t.Log("caller deadline is preserved in the direct foreground outcome")
}

func TestRunnerDirectBufferedMailboxFinalization(t *testing.T) {
	t.Run("work failure abandons", func(t *testing.T) {
		runner, manager := newTestRunner(t)
		wantErr := errors.New("work failed")
		result, err := runner.Run(
			context.Background(),
			&Input{Description: "failed"},
			func(context.Context, background.ExecutionRuntime) (string, error) {
				return "", wantErr
			},
		)
		require.NoError(t, err)
		outcome := requireForegroundResult(t, result)
		require.Equal(t, task.OutcomeFailed, outcome.Status)
		require.Equal(t, wantErr.Error(), outcome.Error)
		waitMailboxState(t, manager, result.ID(), task.MailboxSealed)
		_, err = manager.Get(context.Background(), result.ID())
		require.ErrorIs(t, err, background.ErrNotFound)
	})

	t.Run("caller cancellation abandons", func(t *testing.T) {
		timeout := 0
		runner, manager := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
		})
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		returned := make(chan struct {
			result *RunResult
			err    error
		}, 1)
		go func() {
			result, err := runner.Run(
				ctx,
				&Input{Description: "canceled"},
				func(ctx context.Context, _ background.ExecutionRuntime) (string, error) {
					close(started)
					<-ctx.Done()
					return "", ctx.Err()
				},
			)
			returned <- struct {
				result *RunResult
				err    error
			}{result: result, err: err}
		}()
		<-started
		cancel()
		got := <-returned
		require.NoError(t, got.err)
		outcome := requireForegroundResult(t, got.result)
		require.Equal(t, task.OutcomeCanceled, outcome.Status)
		require.Equal(t, context.Canceled.Error(), outcome.Error)
		waitMailboxState(t, manager, got.result.ID(), task.MailboxSealed)
		_, err := manager.Get(context.Background(), got.result.ID())
		require.ErrorIs(t, err, background.ErrNotFound)
	})
}

func TestRunnerDirectMailboxFinalizationErrorsAreReturned(t *testing.T) {
	newRunner := func(
		t *testing.T,
		store *mailboxFinalizationErrorStore,
	) *Runner {
		t.Helper()
		manager := mustNewBackgroundManager(
			t,
			context.Background(),
			&background.Config{
				Tasks: store,
				IDGen: func(
					context.Context,
					*background.AllocateTaskIDRequest,
				) (string, error) {
					return "task", nil
				},
			},
		)
		runner, err := New(&Config{Manager: manager})
		require.NoError(t, err)
		return runner
	}

	t.Run("seal", func(t *testing.T) {
		wantErr := errors.New("seal failed")
		runner := newRunner(t, &mailboxFinalizationErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			sealErr:       wantErr,
		})
		result, err := runner.Run(
			context.Background(),
			&Input{Description: "completed"},
			func(context.Context, background.ExecutionRuntime) (string, error) {
				return "done", nil
			},
		)
		require.ErrorIs(t, err, wantErr)
		outcome := requireForegroundResult(t, result)
		require.Equal(t, task.OutcomeCompleted, outcome.Status)
	})

	t.Run("abandon", func(t *testing.T) {
		wantErr := errors.New("abandon failed")
		runner := newRunner(t, &mailboxFinalizationErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			abandonErr:    wantErr,
		})
		result, err := runner.Run(
			context.Background(),
			&Input{Description: "failed"},
			func(context.Context, background.ExecutionRuntime) (string, error) {
				return "", errors.New("work failed")
			},
		)
		require.ErrorIs(t, err, wantErr)
		outcome := requireForegroundResult(t, result)
		require.Equal(t, task.OutcomeFailed, outcome.Status)
	})
}

func TestRunnerTaskFirstCallerAbortPolicyWithoutAutoBackground(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		cancelOnAbort bool
	}{
		{name: "detach", cancelOnAbort: false},
		{name: "cancel", cancelOnAbort: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			timeout := 0
			runner, manager := newTestRunner(t, func(config *Config) {
				config.ForegroundTimeoutMs = &timeout
				config.ShouldCancelOnCallerAbort = func(
					context.Context,
					*foreground.CallerAbortInfo,
				) bool {
					return testCase.cancelOnAbort
				}
			})
			type workObservation struct {
				execution task.ExecutionContext
				detached  <-chan struct{}
			}
			started := make(chan workObservation, 1)
			release := make(chan struct{})
			workCanceled := make(chan struct{}, 1)
			callerCtx, cancelCaller := context.WithCancel(context.Background())
			result := make(chan struct {
				runResult *RunResult
				err       error
			}, 1)
			go func() {
				runResult, err := runner.Run(
					callerCtx,
					&Input{Description: testCase.name},
					func(
						ctx context.Context,
						_ background.ExecutionRuntime,
					) (string, error) {
						execution, _ := task.ExecutionContextFromContext(ctx)
						started <- workObservation{
							execution: execution,
							detached:  ProjectionDetached(ctx),
						}
						select {
						case <-release:
							return "done", nil
						case <-ctx.Done():
							workCanceled <- struct{}{}
							return "", ctx.Err()
						}
					},
				)
				result <- struct {
					runResult *RunResult
					err       error
				}{runResult: runResult, err: err}
			}()
			observation := <-started
			require.Equal(t, task.OwnerManager, observation.execution.Owner)
			require.Equal(t, int64(1), observation.execution.Attempt)
			require.NotNil(t, observation.detached)
			cancelCaller()
			returned := <-result
			require.NoError(t, returned.err)
			snapshot := requireTaskResult(t, returned.runResult)
			if testCase.cancelOnAbort {
				require.Equal(t, background.StatusCanceled, snapshot.Status)
				select {
				case <-workCanceled:
				case <-time.After(time.Second):
					t.Fatal("caller-abort policy did not cancel task-owned work")
				}
				return
			}
			require.Equal(
				t,
				background.PublicationOnBackground,
				snapshot.Publication,
			)
			select {
			case <-observation.detached:
			case <-time.After(time.Second):
				t.Fatal("projection detach signal was not closed")
			}
			select {
			case <-workCanceled:
				t.Fatal("detach policy canceled task-owned work")
			default:
			}
			close(release)
			completed := waitTerminal(t, manager, snapshot)
			require.Equal(t, background.StatusCompleted, completed.Status)
		})
	}
}

func TestRunnerStreamTaskFirstCallerAbortPolicyWithoutAutoBackground(t *testing.T) {
	type policyContextKey struct{}
	const policyContextValue = "caller-value"
	for _, testCase := range []struct {
		name          string
		cancelOnAbort bool
	}{
		{name: "detach", cancelOnAbort: false},
		{name: "cancel", cancelOnAbort: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			type policyObservation struct {
				value any
				err   error
				cause error
			}
			policyCalled := make(chan policyObservation, 1)
			timeout := 0
			runner, manager := newTestRunner(t, func(config *Config) {
				config.ForegroundTimeoutMs = &timeout
				config.ShouldCancelOnCallerAbort = func(
					ctx context.Context,
					info *foreground.CallerAbortInfo,
				) bool {
					policyCalled <- policyObservation{
						value: ctx.Value(policyContextKey{}),
						err:   ctx.Err(),
						cause: info.Err,
					}
					return testCase.cancelOnAbort
				}
			})
			started := make(chan task.ExecutionContext, 1)
			release := make(chan struct{})
			workCanceled := make(chan struct{}, 1)
			callerCtx, cancelCaller := context.WithCancel(context.WithValue(
				context.Background(),
				policyContextKey{},
				policyContextValue,
			))
			stream, err := runner.RunStream(
				callerCtx,
				&Input{Description: testCase.name},
				func(
					ctx context.Context,
					_ background.ExecutionRuntime,
				) (*schema.StreamReader[string], error) {
					execution, _ := task.ExecutionContextFromContext(ctx)
					started <- execution
					reader, writer := schema.Pipe[string](1)
					go func() {
						defer writer.Close()
						select {
						case <-release:
							writer.Send("done", nil)
						case <-ctx.Done():
							workCanceled <- struct{}{}
						}
					}()
					return reader, nil
				},
			)
			require.NoError(t, err)
			execution := awaitLocalTestValue(
				t,
				started,
				"stream execution context",
			)
			require.Equal(t, task.OwnerManager, execution.Owner)
			cancelCaller()
			_, recvErr := stream.Recv()
			require.ErrorIs(t, recvErr, io.EOF)
			observation := awaitLocalTestValue(
				t,
				policyCalled,
				"caller-abort policy observation",
			)
			require.Equal(t, policyContextValue, observation.value)
			require.NoError(t, observation.err)
			require.ErrorIs(t, observation.cause, context.Canceled)

			snapshot := onlyTask(t, manager)
			if testCase.cancelOnAbort {
				snapshot = waitTerminal(t, manager, snapshot)
				require.Equal(t, background.StatusCanceled, snapshot.Status)
				select {
				case <-workCanceled:
				case <-time.After(time.Second):
					t.Fatal("caller-abort policy did not cancel streaming work")
				}
				return
			}
			require.Equal(t, background.PublicationOnBackground, snapshot.Publication)
			select {
			case <-workCanceled:
				t.Fatal("detach policy canceled streaming work")
			default:
			}
			close(release)
			require.Equal(
				t,
				background.StatusCompleted,
				waitTerminal(t, manager, snapshot).Status,
			)
		})
	}

	t.Run("detach publication error is sent to reader", func(t *testing.T) {
		publishErr := errors.New("publish failed")
		store := &attackPublishErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			err:           publishErr,
		}
		timeout := 0
		runner, manager := newStage3Runner(t, &background.Config{
			Tasks: store, TaskEvents: store,
			IDGen: func(
				context.Context,
				*background.AllocateTaskIDRequest,
			) (string, error) {
				return "test_1", nil
			},
		}, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
			config.ShouldCancelOnCallerAbort = func(
				context.Context,
				*foreground.CallerAbortInfo,
			) bool {
				return false
			}
		})
		callerCtx, cancelCaller := context.WithCancel(context.Background())
		stream, err := runner.RunStream(
			callerCtx,
			&Input{Description: "publish error"},
			func(
				ctx context.Context,
				_ background.ExecutionRuntime,
			) (*schema.StreamReader[string], error) {
				reader, writer := schema.Pipe[string](1)
				go func() {
					defer writer.Close()
					<-ctx.Done()
				}()
				return reader, nil
			},
		)
		require.NoError(t, err)

		cancelCaller()
		_, recvErr := stream.Recv()
		require.ErrorIs(t, recvErr, publishErr)
		snapshot := onlyTask(t, manager)
		require.Equal(t, background.StatusCanceled, snapshot.Status)
	})
}

func TestRunnerTaskFirstCancelStopsUnderlyingWork(t *testing.T) {
	timeout := 1
	runner, manager := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
		config.ShouldAutoBackground = func(
			context.Context,
			*foreground.CandidateInfo,
		) bool {
			return true
		}
	})
	workStarted := make(chan struct{})
	workCanceled := make(chan struct{})
	result, err := runner.Run(
		context.Background(),
		&Input{Description: "cancel"},
		func(ctx context.Context, _ background.ExecutionRuntime) (string, error) {
			close(workStarted)
			<-ctx.Done()
			close(workCanceled)
			return "", ctx.Err()
		},
	)
	require.NoError(t, err)
	backgroundTask := requireTaskResult(t, result)
	require.Equal(t, background.PublicationOnBackground, backgroundTask.Publication)
	select {
	case <-workStarted:
	case <-time.After(time.Second):
		t.Fatal("underlying work did not start")
	}
	_, err = manager.RequestCancel(
		context.Background(),
		backgroundTask.Spec.ID,
		background.WithCancellationReason("operator stopped task"),
	)
	require.NoError(t, err)
	select {
	case <-workCanceled:
	case <-time.After(time.Second):
		t.Fatal("task cancellation did not reach underlying work")
	}
	canceled := waitTerminal(t, manager, backgroundTask)
	require.Equal(t, background.StatusCanceled, canceled.Status)
	require.Equal(t, "operator stopped task", canceled.ResultError)
}

func TestRunnerStreamProjectsForegroundOutput_BitsUT(t *testing.T) {
	timeout := 0
	runner, manager := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
	})
	stream, err := runner.RunStream(context.Background(), &Input{
		Description: "stream",
	}, streamWork("a", "b", "c"))
	require.NoError(t, err)
	assert.Equal(t, "abc", drain(t, stream))
	waitMailboxState(t, manager, "test_1", task.MailboxSealed)

	_, err = manager.Get(context.Background(), "test_1")
	require.ErrorIs(t, err, background.ErrNotFound)
}

func TestRunnerStreamUsesConfiguredEventPersister(t *testing.T) {
	t.Run("direct foreground has no durable event authority", func(t *testing.T) {
		timeout := 0
		var persistCalls int64
		var observedRuntime background.ExecutionRuntime
		runner, manager := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
			config.EventPersister = background.TaskEventPersisterFunc[string, string](
				func(
					context.Context,
					background.TaskEventScope,
					*background.TaskEventEnvelope[string, string],
					background.TaskEventWriter,
				) error {
					atomic.AddInt64(&persistCalls, 1)
					return errors.New("direct foreground must not persist task events")
				},
			)
		})
		reader, err := runner.RunStream(
			context.Background(),
			&Input{Description: "direct foreground"},
			func(
				ctx context.Context,
				runtime background.ExecutionRuntime,
			) (*schema.StreamReader[string], error) {
				observedRuntime = runtime
				return streamWork("one", "two")(ctx, runtime)
			},
		)
		require.NoError(t, err)
		require.Equal(t, "onetwo", drain(t, reader))
		require.Zero(t, atomic.LoadInt64(&persistCalls))
		require.Nil(t, observedRuntime)
		_, err = manager.Get(context.Background(), "test_1")
		require.ErrorIs(t, err, background.ErrNotFound)
	})

	t.Run("manager-owned stream persists events", func(t *testing.T) {
		runner, manager := newTestRunner(t, func(config *Config) {
			config.EventPersister = prefixedLocalEventPersister{}
		})
		reader, err := runner.RunStream(
			context.Background(),
			&Input{Description: "custom events", RunInBackground: true},
			streamWork("one", "two"),
		)
		require.NoError(t, err)
		_ = drain(t, reader)
		task := waitTerminal(t, manager, onlyTask(t, manager))
		require.Equal(t, background.StatusCompleted, task.Status)

		page, err := manager.ListTaskEvents(
			context.Background(),
			&background.ListTaskEventsRequest{TaskID: task.Spec.ID},
		)
		require.NoError(t, err)
		require.Len(t, page.Parts, 2)
		require.Equal(t, "custom", page.Parts[0].PartID)
		require.Equal(t, "encoded:one", string(page.Parts[0].Data))
		require.Equal(t, "encoded:two", string(page.Parts[1].Data))
	})
}

func TestRunnerStreamTaskFirstConstructionBoundaries(t *testing.T) {
	type startResult struct {
		stream *schema.StreamReader[string]
		err    error
	}
	startBlocked := func(
		t *testing.T,
		runner *Runner,
		ctx context.Context,
		description string,
	) (<-chan struct{}, chan<- struct{}, <-chan startResult) {
		t.Helper()
		entered := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan startResult, 1)
		go func() {
			stream, err := runner.RunStream(
				ctx,
				&Input{Description: description},
				func(
					context.Context,
					background.ExecutionRuntime,
				) (*schema.StreamReader[string], error) {
					close(entered)
					<-release
					return streamWork("done")(context.Background(), nil)
				},
			)
			returned <- startResult{stream: stream, err: err}
		}()
		return entered, release, returned
	}
	awaitStart := func(t *testing.T, entered <-chan struct{}) {
		t.Helper()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("stream constructor did not start")
		}
	}
	awaitResult := func(t *testing.T, returned <-chan startResult) startResult {
		t.Helper()
		select {
		case result := <-returned:
			return result
		case <-time.After(time.Second):
			t.Fatal("RunStream did not resolve the construction boundary")
			return startResult{}
		}
	}

	t.Run("timeout auto-backgrounds a blocked constructor", func(t *testing.T) {
		timeout := 20
		runner, manager := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
			config.ShouldAutoBackground = func(
				context.Context,
				*foreground.CandidateInfo,
			) bool {
				return true
			}
			config.BackgroundNotice = func(context.Context, NoticeInfo) string {
				return "backgrounded"
			}
		})
		entered, release, returned := startBlocked(
			t, runner, context.Background(), "timeout background",
		)
		awaitStart(t, entered)
		result := awaitResult(t, returned)
		require.NoError(t, result.err)
		require.Equal(t, "backgrounded", drain(t, result.stream))
		snapshot := onlyTask(t, manager)
		require.Equal(t, background.PublicationOnBackground, snapshot.Publication)
		require.Equal(t, background.StatusRunning, snapshot.Status)
		close(release)
		require.Equal(
			t,
			background.StatusCompleted,
			waitTerminal(t, manager, snapshot).Status,
		)
	})

	t.Run("timeout cancels when auto-background is rejected", func(t *testing.T) {
		timeout := 20
		runner, manager := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
			config.ShouldAutoBackground = func(
				context.Context,
				*foreground.CandidateInfo,
			) bool {
				return false
			}
		})
		entered, release, returned := startBlocked(
			t, runner, context.Background(), "timeout cancel",
		)
		awaitStart(t, entered)
		result := awaitResult(t, returned)
		require.NoError(t, result.err)
		_, recvErr := result.stream.Recv()
		require.ErrorIs(t, recvErr, context.DeadlineExceeded)
		snapshot := waitTerminal(t, manager, onlyTask(t, manager))
		require.Equal(t, background.StatusCanceled, snapshot.Status)
		require.Equal(t, "timed out after 20ms", snapshot.ResultError)
		close(release)
	})

	for _, testCase := range []struct {
		name          string
		cancelOnAbort bool
		wantStatus    background.Status
	}{
		{
			name:          "caller abort detaches",
			cancelOnAbort: false,
			wantStatus:    background.StatusRunning,
		},
		{
			name:          "caller abort cancels",
			cancelOnAbort: true,
			wantStatus:    background.StatusCanceled,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			timeout := 0
			runner, manager := newTestRunner(t, func(config *Config) {
				config.ForegroundTimeoutMs = &timeout
				config.ShouldCancelOnCallerAbort = func(
					context.Context,
					*foreground.CallerAbortInfo,
				) bool {
					return testCase.cancelOnAbort
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			entered, release, returned := startBlocked(
				t, runner, ctx, testCase.name,
			)
			awaitStart(t, entered)
			cancel()
			result := awaitResult(t, returned)
			require.NoError(t, result.err)
			_, recvErr := result.stream.Recv()
			require.ErrorIs(t, recvErr, io.EOF)
			snapshot := onlyTask(t, manager)
			require.Equal(t, testCase.wantStatus, snapshot.Status)
			if testCase.cancelOnAbort {
				require.Equal(
					t,
					"caller aborted foreground projection",
					snapshot.ResultError,
				)
			} else {
				require.Equal(
					t,
					background.PublicationOnBackground,
					snapshot.Publication,
				)
			}
			close(release)
			if !testCase.cancelOnAbort {
				require.Equal(
					t,
					background.StatusCompleted,
					waitTerminal(t, manager, snapshot).Status,
				)
			}
		})
	}
}

func TestRunnerDirectStreamForegroundTimeoutStartsAfterConstruction_BitsUT(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		configTimeout int
		inputTimeout  *int
	}{
		{name: "config timeout", configTimeout: 20},
		{
			name:          "input timeout overrides config",
			configTimeout: 1000,
			inputTimeout:  func() *int { value := 20; return &value }(),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runner, manager := newTestRunner(t, func(config *Config) {
				config.ForegroundTimeoutMs = &testCase.configTimeout
			})
			stream, err := runner.RunStream(
				context.Background(),
				&Input{
					Description:         testCase.name,
					ForegroundTimeoutMs: testCase.inputTimeout,
				},
				func(
					ctx context.Context,
					_ background.ExecutionRuntime,
				) (*schema.StreamReader[string], error) {
					reader, writer := schema.Pipe[string](1)
					go func() {
						<-ctx.Done()
						writer.Close()
					}()
					return reader, nil
				},
			)
			require.NoError(t, err)
			_, recvErr := stream.Recv()
			require.ErrorIs(t, recvErr, context.DeadlineExceeded)
			waitMailboxState(t, manager, "test_1", task.MailboxSealed)
		})
	}
}

func TestRunnerStreamErrorFailsProjectionAndTask_BitsUT(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		taskFirst bool
	}{
		{name: "direct", taskFirst: false},
		{name: "task-first", taskFirst: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			wantErr := errors.New("boom")
			runner, manager := newTestRunner(t, func(config *Config) {
				if testCase.taskFirst {
					config.ShouldAutoBackground = func(
						context.Context,
						*foreground.CandidateInfo,
					) bool {
						return true
					}
				}
			})
			stream, err := runner.RunStream(
				context.Background(),
				&Input{Description: "stream"},
				func(
					context.Context,
					background.ExecutionRuntime,
				) (*schema.StreamReader[string], error) {
					reader, writer := schema.Pipe[string](1)
					go func() {
						writer.Send("", wantErr)
						writer.Close()
					}()
					return reader, nil
				},
			)
			require.NoError(t, err)
			_, recvErr := stream.Recv()
			require.ErrorIs(t, recvErr, wantErr)
			waitMailboxState(t, manager, "test_1", task.MailboxSealed)
			if testCase.taskFirst {
				failed := waitTerminal(t, manager, onlyTask(t, manager))
				require.Equal(t, background.StatusFailed, failed.Status)
				require.Contains(t, failed.ResultError, wantErr.Error())
			} else {
				_, err = manager.Get(context.Background(), "test_1")
				require.ErrorIs(t, err, background.ErrNotFound)
			}
		})
	}
}

func TestRunnerTaskFirstStreamBoundaryWaitError(t *testing.T) {
	waitErr := errors.New("wait boundary failed")
	store := &waitVersionErrorStore{
		InMemoryStore: background.NewInMemoryStore(nil),
		err:           waitErr,
	}
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store, TaskEvents: store,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	timeout := 100
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
	source, sourceWriter := schema.Pipe[string](1)
	stream, err := runner.RunStream(
		context.Background(),
		&Input{Description: "wait error"},
		func(context.Context, background.ExecutionRuntime) (*schema.StreamReader[string], error) {
			return source, nil
		},
	)
	if err != nil {
		sourceWriter.Close()
		require.Nil(t, stream)
		require.ErrorIs(t, err, waitErr)
		return
	}
	require.NotNil(t, stream)
	sourceWriter.Close()
	_, recvErr := stream.Recv()
	require.ErrorIs(t, recvErr, waitErr)
}

func TestForegroundStreamDrainBoundaries(t *testing.T) {
	t.Run("drain error", func(t *testing.T) {
		chunks := make(chan streamChunk, 1)
		wantErr := errors.New("drain failed")
		chunks <- streamChunk{err: wantErr}
		close(chunks)
		reader, writer := schema.Pipe[string](1)
		callerClosed, err := drainForegroundChunks(writer, chunks)
		require.False(t, callerClosed)
		require.ErrorIs(t, err, wantErr)
		reader.Close()
	})

	t.Run("caller close", func(t *testing.T) {
		chunks := make(chan streamChunk, 1)
		chunks <- streamChunk{text: "ignored"}
		close(chunks)
		reader, writer := schema.Pipe[string](1)
		reader.Close()
		callerClosed, err := drainForegroundChunks(writer, chunks)
		require.True(t, callerClosed)
		require.NoError(t, err)
	})

	t.Run("terminal error after drain", func(t *testing.T) {
		runner, manager := newTestRunner(t)
		registered, err := manager.RegisterMailbox(
			context.Background(),
			&task.RegisterMailboxRequest{
				CandidateTaskID: "terminal-error",
				InvocationID:    "terminal-error",
			},
		)
		require.NoError(t, err)
		chunks := make(chan streamChunk, 1)
		chunks <- streamChunk{text: "prefix"}
		close(chunks)
		resultCh := make(chan struct {
			value string
			err   error
		}, 1)
		wantErr := errors.New("terminal failed")
		resultCh <- struct {
			value string
			err   error
		}{err: wantErr}
		reader, writer := schema.Pipe[string](1)
		go runner.projectForegroundStream(&foregroundStreamProjection{
			ctx:      context.Background(),
			chunks:   chunks,
			resultCh: resultCh,
			writer:   writer,
			cancel:   func() {},
			finalizer: taskfirst.NewForegroundMailboxFinalizer(
				manager,
				registered.Mailbox.TaskID,
				registered.Mailbox.Generation,
				registered.Mailbox.ConsumedCursor,
			),
		})
		value, recvErr := reader.Recv()
		require.NoError(t, recvErr)
		require.Equal(t, "prefix", value)
		_, recvErr = reader.Recv()
		require.ErrorIs(t, recvErr, wantErr)
	})
}

func TestRunnerDirectStreamReaderCloseAbandonsMailbox(t *testing.T) {
	runner, manager := newTestRunner(t)
	source, sourceWriter := schema.Pipe[string](1)
	stream, err := runner.RunStream(
		context.Background(),
		&Input{Description: "reader close"},
		func(context.Context, background.ExecutionRuntime) (*schema.StreamReader[string], error) {
			return source, nil
		},
	)
	require.NoError(t, err)
	stream.Close()
	require.False(t, sourceWriter.Send("late", nil))
	sourceWriter.Close()
	waitMailboxState(t, manager, "test_1", task.MailboxSealed)
}

func TestRunnerDirectStreamCompletionPreservesPendingInput(t *testing.T) {
	runner, manager := newTestRunner(t)
	stream, err := runner.RunStream(
		context.Background(),
		&Input{Description: "pending input"},
		func(ctx context.Context, _ background.ExecutionRuntime) (*schema.StreamReader[string], error) {
			execution, ok := task.ExecutionContextFromContext(ctx)
			if !ok {
				return nil, errors.New("execution context is missing")
			}
			reader, writer := schema.Pipe[string](1)
			go func() {
				writer.Send("done", nil)
				_, sendErr := manager.SendInput(
					context.Background(),
					&task.SendInputRequest{
						TaskID: execution.TaskID,
						Input: task.Input{
							EventID: "pending", Kind: "message", Data: []byte("keep"),
						},
					},
				)
				if sendErr != nil {
					writer.Send("", sendErr)
				}
				writer.Close()
			}()
			return reader, nil
		},
	)
	require.NoError(t, err)
	value, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "done", value)
	_, err = stream.Recv()
	require.ErrorIs(t, err, task.ErrInputsPending)

	mailbox, err := manager.GetMailbox(context.Background(), "test_1")
	require.NoError(t, err)
	require.Equal(t, task.MailboxForeground, mailbox.State)
	require.Equal(t, int64(1), mailbox.LatestSequence)
	require.Zero(t, mailbox.ConsumedCursor)
}

func TestRunnerStreamConstructionFailures(t *testing.T) {
	runner, manager := newTestRunner(t)
	wantErr := errors.New("construct failed")
	cases := []struct {
		name string
		work StreamWorkFunc
	}{
		{name: "work error", work: func(
			context.Context, background.ExecutionRuntime,
		) (*schema.StreamReader[string], error) {
			return nil, wantErr
		}},
		{name: "nil reader", work: func(
			context.Context, background.ExecutionRuntime,
		) (*schema.StreamReader[string], error) {
			return nil, nil
		}},
		{name: "panic", work: func(
			context.Context, background.ExecutionRuntime,
		) (*schema.StreamReader[string], error) {
			panic(wantErr)
		}},
	}
	for index, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stream, err := runner.RunStream(
				context.Background(), &Input{Description: testCase.name}, testCase.work,
			)
			require.Error(t, err)
			require.Nil(t, stream)
			waitMailboxState(
				t,
				manager,
				fmt.Sprintf("test_%d", index+1),
				task.MailboxSealed,
			)
		})
	}
	stream, err := runner.RunStream(context.Background(), nil, streamWork("ignored"))
	require.Error(t, err)
	require.Nil(t, stream)
	stream, err = runner.RunStream(context.Background(), &Input{}, nil)
	require.Error(t, err)
	require.Nil(t, stream)
}

func TestAttack_EarlyStreamConstructionErrorWinsTerminalRace(t *testing.T) {
	runner, _ := newTestRunner(t)
	wantErr := errors.New("immediate construction failure")
	for iteration := 0; iteration < 32; iteration++ {
		stream, err := runner.RunStream(
			context.Background(),
			&Input{Description: "immediate failure"},
			func(
				context.Context,
				background.ExecutionRuntime,
			) (*schema.StreamReader[string], error) {
				return nil, wantErr
			},
		)
		require.ErrorIs(t, err, wantErr)
		require.Nil(t, stream)
	}
	t.Log("the original stream construction error won every terminal race")
}

func TestProjectStreamTerminalBoundaries(t *testing.T) {
	runner, _ := newTestRunner(t)
	input := &Input{}

	t.Run("run error", func(t *testing.T) {
		chunks := make(chan streamChunk)
		runDone := make(chan runResult, 1)
		wantErr := errors.New("run failed")
		runDone <- runResult{err: wantErr}
		reader, writer := schema.Pipe[string](1)
		done := make(chan struct{})
		go func() {
			runner.projectStream(
				context.Background(), input, "task", chunks, runDone, writer,
			)
			close(done)
		}()
		_, err := reader.Recv()
		require.ErrorIs(t, err, wantErr)
		close(chunks)
		<-done
	})

	t.Run("chunk error", func(t *testing.T) {
		chunks := make(chan streamChunk, 1)
		runDone := make(chan runResult)
		wantErr := errors.New("chunk failed")
		chunks <- streamChunk{err: wantErr}
		reader, writer := schema.Pipe[string](1)
		done := make(chan struct{})
		go func() {
			runner.projectStream(
				context.Background(), input, "task", chunks, runDone, writer,
			)
			close(done)
		}()
		_, err := reader.Recv()
		require.ErrorIs(t, err, wantErr)
		close(chunks)
		<-done
	})

	t.Run("caller closes projection", func(t *testing.T) {
		chunks := make(chan streamChunk, 1)
		runDone := make(chan runResult)
		chunks <- streamChunk{text: "ignored"}
		reader, writer := schema.Pipe[string](1)
		reader.Close()
		done := make(chan struct{})
		go func() {
			runner.projectStream(
				context.Background(), input, "task", chunks, runDone, writer,
			)
			close(done)
		}()
		close(chunks)
		<-done
	})

	t.Run("context canceled", func(t *testing.T) {
		store := &countingGetStore{
			InMemoryStore: background.NewInMemoryStore(nil),
		}
		manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
			Tasks: store, TaskEvents: store,
		})
		runner := &Runner{manager: manager}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		chunks := make(chan streamChunk)
		runDone := make(chan runResult)
		reader, writer := schema.Pipe[string](1)
		done := make(chan struct{})
		go func() {
			runner.projectStream(ctx, input, "task", chunks, runDone, writer)
			close(done)
		}()
		_, err := reader.Recv()
		require.ErrorIs(t, err, io.EOF)
		time.Sleep(20 * time.Millisecond)
		require.Equal(t, int64(1), atomic.LoadInt64(&store.getCount))
		close(chunks)
		<-done
	})
}

func TestRunnerStreamBackgroundNotices(t *testing.T) {
	newRunner := func(t *testing.T, auto bool) (*Runner, *background.Manager) {
		t.Helper()
		timeout := 1
		return newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
			config.BackgroundNotice = func(_ context.Context, info NoticeInfo) string {
				return fmt.Sprintf("notice:%t", info.AutoBackgrounded)
			}
			if auto {
				config.ShouldAutoBackground = func(context.Context, *foreground.CandidateInfo) bool {
					return true
				}
			}
		})
	}

	t.Run("explicit background", func(t *testing.T) {
		runner, manager := newRunner(t, false)
		release := make(chan struct{})
		stream, err := runner.RunStream(context.Background(), &Input{
			Description: "background", RunInBackground: true,
		}, gatedStreamWork(release))
		require.NoError(t, err)
		require.Equal(t, "notice:false", drain(t, stream))
		close(release)
		require.Equal(t, background.StatusCompleted,
			waitTerminal(t, manager, onlyTask(t, manager)).Status)
	})

	t.Run("preview expires", func(t *testing.T) {
		runner, manager := newRunner(t, false)
		release := make(chan struct{})
		stream, err := runner.RunStream(context.Background(), &Input{
			Description: "preview", RunInBackground: true, BackgroundStartupPreviewMs: 1,
		}, gatedStreamWork(release))
		require.NoError(t, err)
		require.Equal(t, "notice:false", drain(t, stream))
		close(release)
		require.Equal(t, background.StatusCompleted,
			waitTerminal(t, manager, onlyTask(t, manager)).Status)
	})

	t.Run("auto background", func(t *testing.T) {
		runner, manager := newRunner(t, true)
		release := make(chan struct{})
		stream, err := runner.RunStream(context.Background(), &Input{
			Description: "auto",
		}, gatedStreamWork(release))
		require.NoError(t, err)
		require.Equal(t, "notice:true", drain(t, stream))
		close(release)
		require.Equal(t, background.StatusCompleted,
			waitTerminal(t, manager, onlyTask(t, manager)).Status)
	})
}

func TestRunnerStreamExplicitBackgroundReturnsBeforeStreamConstruction_BitsUT(t *testing.T) {
	runner, manager := newTestRunner(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	type result struct {
		stream *schema.StreamReader[string]
		err    error
	}
	returned := make(chan result, 1)
	go func() {
		stream, err := runner.RunStream(context.Background(), &Input{
			Description: "blocking construction", RunInBackground: true,
			BackgroundStartupPreviewMs: 1,
		}, func(context.Context, background.ExecutionRuntime) (*schema.StreamReader[string], error) {
			close(entered)
			<-release
			return streamWork("done")(context.Background(), nil)
		})
		returned <- result{stream: stream, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("background work did not begin")
	}
	select {
	case got := <-returned:
		require.NoError(t, got.err)
		require.NotNil(t, got.stream)
		require.Contains(t, drain(t, got.stream), "running in the background")
	case <-time.After(time.Second):
		t.Fatal("explicit background run waited for stream construction")
	}

	close(release)
	released = true
	require.Equal(t, background.StatusCompleted,
		waitTerminal(t, manager, onlyTask(t, manager)).Status)
}

func TestRunnerLocalContracts(t *testing.T) {
	runner, manager := newTestRunner(t)
	require.Same(t, manager, runner.Manager())
	require.Nil(t, ProjectionDetached(nil))
	require.Nil(t, ProjectionDetached(context.Background()))
	var nilRunner *Runner
	require.Nil(t, nilRunner.Manager())
	require.Error(t, runner.executor.ValidateSpec(background.Spec{
		ExecutorKey: "wrong",
	}))

	task := &background.TaskSnapshot{Spec: background.Spec{
		ID: "task", Kind: "bash", OutputFile: "/tasks/output",
	}}
	notice := defaultBackgroundNotice(context.Background(), NoticeInfo{Task: task})
	require.Contains(t, notice, "is running in the background")
	require.Contains(t, notice, "/tasks/output")
	require.Contains(t, notice, "task_output")
	require.NotContains(t, notice, "you will be notified")
	task.Spec.NotifySession = true
	notice = defaultBackgroundNotice(context.Background(), NoticeInfo{
		Task: task, AutoBackgrounded: true,
	})
	require.Contains(t, notice, "moved to the background")
	require.Contains(t, notice, "you will be notified")

	work := func(context.Context, background.ExecutionRuntime) (string, error) {
		return "done", nil
	}
	result, err := nilRunner.Run(context.Background(), &Input{}, work)
	require.Error(t, err)
	require.Nil(t, result)
	result, err = runner.Run(context.Background(), nil, work)
	require.Error(t, err)
	require.Nil(t, result)
	result, err = runner.Run(context.Background(), &Input{}, nil)
	require.Error(t, err)
	require.Nil(t, result)
	result, err = runner.Run(context.Background(), &Input{
		NotifySession: true,
	}, work)
	require.NoError(t, err)
	require.NotNil(t, result)

	pending, err := runner.submit(
		context.Background(), &Input{Kind: "test", Description: "pending"}, work,
	)
	require.NoError(t, err)
	require.Contains(t, runner.executor.works, pending.Spec.ID)
	runner.removeUnstarted(pending.Spec.ID)
	require.NotContains(t, runner.executor.works, pending.Spec.ID)
	runner.removeUnstarted("missing")
}

func gatedStreamWork(release <-chan struct{}) StreamWorkFunc {
	return func(context.Context, background.ExecutionRuntime) (*schema.StreamReader[string], error) {
		reader, writer := schema.Pipe[string](1)
		go func() {
			<-release
			writer.Send("done", nil)
			writer.Close()
		}()
		return reader, nil
	}
}

func streamWork(chunks ...string) StreamWorkFunc {
	return func(context.Context, background.ExecutionRuntime) (*schema.StreamReader[string], error) {
		reader, writer := schema.Pipe[string](len(chunks))
		go func() {
			for _, chunk := range chunks {
				writer.Send(chunk, nil)
			}
			writer.Close()
		}()
		return reader, nil
	}
}

func drain(t *testing.T, reader *schema.StreamReader[string]) string {
	t.Helper()
	type drainResult struct {
		value string
		err   error
	}
	done := make(chan drainResult, 1)
	go func() {
		var result strings.Builder
		for {
			chunk, err := reader.Recv()
			if err != nil {
				done <- drainResult{value: result.String(), err: err}
				return
			}
			result.WriteString(chunk)
		}
	}()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case result := <-done:
		reader.Close()
		require.ErrorIs(t, result.err, io.EOF)
		return result.value
	case <-timer.C:
		reader.Close()
		t.Fatal("local stream did not terminate within 1 second")
		return ""
	}
}

func onlyTask(t *testing.T, manager *background.Manager) *background.TaskSnapshot {
	t.Helper()
	task, err := manager.Get(context.Background(), "test_1")
	require.NoError(t, err)
	return task
}
