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
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
	"github.com/cloudwego/eino/schema"
)

type stage3CreateErrorStore struct {
	*background.InMemoryStore
	err error
}

func (s *stage3CreateErrorStore) Create(
	context.Context,
	*background.CreateTaskRequest,
) (*background.TaskSnapshot, error) {
	return nil, s.err
}

type stage3BoundaryErrorStore struct {
	*background.InMemoryStore
	release chan struct{}
	err     error
}

func (s *stage3BoundaryErrorStore) WaitForTaskVersion(
	context.Context,
	*background.WaitForTaskVersionRequest,
) (*background.TaskSnapshot, error) {
	<-s.release
	return nil, s.err
}

func newStage3Runner(
	t *testing.T,
	config *background.Config,
	localConfig func(*Config),
) (*Runner, *background.Manager) {
	t.Helper()
	manager := mustNewBackgroundManager(t, context.Background(), config)
	runnerConfig := &Config{Manager: manager}
	if localConfig != nil {
		localConfig(runnerConfig)
	}
	runner, err := New(runnerConfig)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, manager.Close(ctx))
	})
	return runner, manager
}

func TestRunnerNewSpec(t *testing.T) {
	t.Run("validates runner and input", func(t *testing.T) {
		var nilRunner *Runner
		_, err := nilRunner.newSpec(context.Background(), &Input{})
		require.EqualError(t, err, "task/local: runner and input are required")

		_, err = (&Runner{}).newSpec(context.Background(), &Input{})
		require.EqualError(t, err, "task/local: runner and input are required")

		runner, _ := newTestRunner(t)
		_, err = runner.newSpec(context.Background(), nil)
		require.EqualError(t, err, "task/local: runner and input are required")
	})

	t.Run("propagates allocation error", func(t *testing.T) {
		wantErr := errors.New("allocate failed")
		runner, _ := newStage3Runner(t, &background.Config{
			IDGen: func(
				context.Context,
				*background.AllocateTaskIDRequest,
			) (string, error) {
				return "", wantErr
			},
		}, nil)

		spec, err := runner.newSpec(context.Background(), &Input{Kind: "tool"})
		require.Equal(t, background.Spec{}, spec)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("derives nested identity and copies payload", func(t *testing.T) {
		runner, _ := newTestRunner(t)
		payload := []byte("payload")
		ctx := task.WithExecutionContext(
			context.Background(),
			task.ExecutionContext{
				TaskID:        "parent",
				Owner:         task.OwnerManager,
				Generation:    3,
				Attempt:       2,
				RootSessionID: "root-from-parent",
			},
		)
		spec, err := runner.newSpec(ctx, &Input{
			Description:   "description",
			Kind:          "kind",
			Payload:       payload,
			OutputFile:    "output",
			SessionID:     "ignored-child-root",
			NotifySession: true,
		})
		require.NoError(t, err)
		require.Equal(t, "test_1", spec.ID)
		require.Equal(t, executorKey, spec.ExecutorKey)
		require.Equal(t, "kind", spec.Kind)
		require.Equal(t, "description", spec.Description)
		require.Equal(t, "output", spec.OutputFile)
		require.Equal(t, "parent", spec.ParentTaskID)
		require.Equal(t, "root-from-parent", spec.RootSessionID)
		require.True(t, spec.NotifySession)
		require.Equal(t, []byte("payload"), spec.Payload)

		payload[0] = 'X'
		require.Equal(t, []byte("payload"), spec.Payload)
	})
}

func TestRunnerSubmit(t *testing.T) {
	work := func(context.Context, background.ExecutionRuntime) (string, error) {
		return "done", nil
	}

	t.Run("rejects invalid runner input and work", func(t *testing.T) {
		var nilRunner *Runner
		snapshot, err := nilRunner.submit(context.Background(), &Input{}, work)
		require.Nil(t, snapshot)
		require.EqualError(t, err, "task/local: runner, input, and work are required")

		runner, _ := newTestRunner(t)
		for _, testCase := range []struct {
			name   string
			runner *Runner
			input  *Input
			work   WorkFunc
		}{
			{name: "missing manager", runner: &Runner{}, input: &Input{}, work: work},
			{
				name: "missing executor",
				runner: &Runner{
					manager: runner.manager,
				},
				input: &Input{},
				work:  work,
			},
			{name: "missing input", runner: runner, work: work},
			{name: "missing work", runner: runner, input: &Input{}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				snapshot, err := testCase.runner.submit(
					context.Background(),
					testCase.input,
					testCase.work,
				)
				require.Nil(t, snapshot)
				require.EqualError(
					t,
					err,
					"task/local: runner, input, and work are required",
				)
			})
		}
	})

	t.Run("propagates allocation error", func(t *testing.T) {
		wantErr := errors.New("allocate failed")
		runner, _ := newStage3Runner(t, &background.Config{
			IDGen: func(
				context.Context,
				*background.AllocateTaskIDRequest,
			) (string, error) {
				return "", wantErr
			},
		}, nil)

		snapshot, err := runner.submit(
			context.Background(),
			&Input{Kind: "tool"},
			work,
		)
		require.Nil(t, snapshot)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("propagates manager submit error", func(t *testing.T) {
		wantErr := errors.New("create failed")
		store := &stage3CreateErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			err:           wantErr,
		}
		runner, _ := newStage3Runner(t, &background.Config{
			Tasks: store, TaskEvents: store,
			IDGen: func(
				context.Context,
				*background.AllocateTaskIDRequest,
			) (string, error) {
				return "task", nil
			},
		}, nil)

		snapshot, err := runner.submit(
			context.Background(),
			&Input{Description: "submit error"},
			work,
		)
		require.Nil(t, snapshot)
		require.ErrorIs(t, err, wantErr)
		_, resolveErr := runner.executor.resolve(background.Spec{ID: "task"})
		require.EqualError(
			t,
			resolveErr,
			"task/local: work is unavailable after process loss",
		)
	})

	t.Run("submits valid work", func(t *testing.T) {
		runner, _ := newTestRunner(t)
		snapshot, err := runner.submit(
			context.Background(),
			&Input{Description: "valid"},
			work,
		)
		require.NoError(t, err)
		require.NotNil(t, snapshot)
		require.Equal(t, background.StatusPending, snapshot.Status)
		resolved, resolveErr := runner.executor.resolve(snapshot.Spec)
		require.NoError(t, resolveErr)
		require.NotNil(t, resolved)
		runner.executor.remove(snapshot.Spec.ID)
	})
}

func TestRunnerSubmitSpec(t *testing.T) {
	work := func(context.Context, background.ExecutionRuntime) (string, error) {
		return "done", nil
	}

	t.Run("validates work before submission", func(t *testing.T) {
		runner, _ := newTestRunner(t)
		snapshot, err := runner.submitSpec(
			context.Background(),
			background.Spec{ID: "task", ExecutorKey: executorKey},
			nil,
		)
		require.Nil(t, snapshot)
		require.EqualError(t, err, "task/local: task id and work are required")
	})

	t.Run("removes work after manager submit error", func(t *testing.T) {
		wantErr := errors.New("create failed")
		store := &stage3CreateErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			err:           wantErr,
		}
		runner, _ := newStage3Runner(t, &background.Config{
			Tasks: store, TaskEvents: store,
		}, nil)
		spec := background.Spec{ID: "task", ExecutorKey: executorKey}

		snapshot, err := runner.submitSpec(context.Background(), spec, work)
		require.Nil(t, snapshot)
		require.ErrorIs(t, err, wantErr)
		_, resolveErr := runner.executor.resolve(spec)
		require.EqualError(
			t,
			resolveErr,
			"task/local: work is unavailable after process loss",
		)
	})

	t.Run("rejects undelivered error without persisted task", func(t *testing.T) {
		store := &stage3CreateErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			err:           background.ErrTaskCreatedEventUndelivered,
		}
		runner, _ := newStage3Runner(t, &background.Config{
			Tasks: store, TaskEvents: store,
		}, nil)
		spec := background.Spec{ID: "task", ExecutorKey: executorKey}

		snapshot, err := runner.submitSpec(context.Background(), spec, work)
		require.Nil(t, snapshot)
		require.EqualError(
			t,
			err,
			"task/local: task-created delivery failed without persisted task",
		)
		_, resolveErr := runner.executor.resolve(spec)
		require.EqualError(
			t,
			resolveErr,
			"task/local: work is unavailable after process loss",
		)
	})

	t.Run("keeps work when only immediate delivery fails", func(t *testing.T) {
		sendErr := errors.New("timeline unavailable")
		store := background.NewInMemoryStore(nil)
		runner, _ := newStage3Runner(t, &background.Config{
			Tasks: store, TaskEvents: store,
			SendTaskCreatedEvent: func(
				context.Context,
				*background.TaskSnapshot,
			) error {
				return sendErr
			},
		}, nil)
		spec := background.Spec{
			ID: "task", ExecutorKey: executorKey,
			RootSessionID: "session", NotifySession: true,
		}

		snapshot, err := runner.submitSpec(context.Background(), spec, work)
		require.NoError(t, err)
		require.NotNil(t, snapshot)
		require.Equal(t, background.StatusPending, snapshot.Status)
		require.Equal(t, background.PublicationOnCreate, snapshot.Publication)
		resolved, resolveErr := runner.executor.resolve(spec)
		require.NoError(t, resolveErr)
		require.NotNil(t, resolved)
		runner.executor.remove(spec.ID)
	})
}

func TestRunnerStartTask(t *testing.T) {
	work := func(context.Context, background.ExecutionRuntime) (string, error) {
		return "done", nil
	}

	t.Run("submit failure removes registered work", func(t *testing.T) {
		wantErr := errors.New("create failed")
		store := &stage3CreateErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			err:           wantErr,
		}
		runner, _ := newStage3Runner(t, &background.Config{
			Tasks: store, TaskEvents: store,
		}, nil)
		spec := background.Spec{
			ID: "start-failure", ExecutorKey: executorKey, Kind: "test",
		}

		execution, err := runner.startTask(
			context.Background(),
			&Input{},
			spec,
			work,
		)
		require.Nil(t, execution)
		require.ErrorIs(t, err, wantErr)
		_, resolveErr := runner.executor.resolve(spec)
		require.EqualError(
			t,
			resolveErr,
			"task/local: work is unavailable after process loss",
		)
	})

	t.Run("successful submission reaches terminal boundary", func(t *testing.T) {
		runner, _ := newTestRunner(t)
		timeout := 500
		execution, err := runner.startTask(
			context.Background(),
			&Input{ForegroundTimeoutMs: &timeout},
			background.Spec{
				ID: "start-success", ExecutorKey: executorKey, Kind: "test",
			},
			work,
		)
		require.NoError(t, err)
		require.Equal(
			t,
			background.StatusPending,
			execution.Initial().Status,
		)

		waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		snapshot, err := execution.WaitBoundary(waitCtx)
		require.NoError(t, err)
		require.Equal(t, background.StatusCompleted, snapshot.Status)
		require.Equal(t, []byte("done"), snapshot.ResultData)
	})
}

func TestRunnerAwaitTaskFirstStreamConstructionRaces(t *testing.T) {
	t.Run("constructor error wins terminal boundary race", func(t *testing.T) {
		runner, _ := newTestRunner(t, func(config *Config) {
			config.ShouldAutoBackground = func(
				context.Context,
				*foreground.CandidateInfo,
			) bool {
				return true
			}
		})
		wantErr := errors.New("constructor failed")

		for iteration := 0; iteration < 32; iteration++ {
			stream, err := runner.RunStream(
				context.Background(),
				&Input{Description: "constructor failure"},
				func(
					context.Context,
					background.ExecutionRuntime,
				) (*schema.StreamReader[string], error) {
					return nil, wantErr
				},
			)
			require.Nil(t, stream)
			require.EqualError(t, err, wantErr.Error())
		}
	})

	t.Run("boundary error resolves blocked constructor", func(t *testing.T) {
		waitErr := errors.New("boundary failed")
		boundaryRelease := make(chan struct{}, 1)
		store := &stage3BoundaryErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			release:       boundaryRelease,
			err:           waitErr,
		}
		runner, _ := newStage3Runner(t, &background.Config{
			Tasks: store, TaskEvents: store,
		}, func(config *Config) {
			config.ShouldAutoBackground = func(
				context.Context,
				*foreground.CandidateInfo,
			) bool {
				return true
			}
		})
		entered := make(chan struct{}, 1)
		constructorRelease := make(chan struct{}, 1)
		returned := make(chan struct {
			stream *schema.StreamReader[string]
			err    error
		}, 1)
		go func() {
			stream, err := runner.RunStream(
				context.Background(),
				&Input{Description: "blocked constructor"},
				func(
					context.Context,
					background.ExecutionRuntime,
				) (*schema.StreamReader[string], error) {
					entered <- struct{}{}
					<-constructorRelease
					reader, writer := schema.Pipe[string](1)
					writer.Close()
					return reader, nil
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
		boundaryRelease <- struct{}{}
		select {
		case result := <-returned:
			require.Nil(t, result.stream)
			require.ErrorIs(t, result.err, waitErr)
			require.EqualError(t, result.err, waitErr.Error())
		case <-time.After(time.Second):
			t.Fatal("boundary error did not resolve stream construction")
		}
		constructorRelease <- struct{}{}
	})
}

func TestDrainChunks(t *testing.T) {
	t.Run("nil channel is complete", func(t *testing.T) {
		reader, writer := schema.Pipe[string](1)
		require.True(t, drainChunks(writer, nil))
		writer.Close()
		_, err := reader.Recv()
		require.ErrorIs(t, err, io.EOF)
	})

	t.Run("closed channel drains all chunks", func(t *testing.T) {
		chunks := make(chan streamChunk, 2)
		chunks <- streamChunk{text: "first"}
		chunks <- streamChunk{text: "second"}
		close(chunks)
		reader, writer := schema.Pipe[string](2)

		require.True(t, drainChunks(writer, chunks))
		writer.Close()
		first, err := reader.Recv()
		require.NoError(t, err)
		require.Equal(t, "first", first)
		second, err := reader.Recv()
		require.NoError(t, err)
		require.Equal(t, "second", second)
		_, err = reader.Recv()
		require.ErrorIs(t, err, io.EOF)
	})

	t.Run("chunk error is forwarded exactly", func(t *testing.T) {
		wantErr := errors.New("chunk failed")
		chunks := make(chan streamChunk, 2)
		chunks <- streamChunk{text: "prefix"}
		chunks <- streamChunk{err: wantErr}
		close(chunks)
		reader, writer := schema.Pipe[string](2)

		require.False(t, drainChunks(writer, chunks))
		writer.Close()
		prefix, err := reader.Recv()
		require.NoError(t, err)
		require.Equal(t, "prefix", prefix)
		_, err = reader.Recv()
		require.ErrorIs(t, err, wantErr)
		require.EqualError(t, err, wantErr.Error())
	})

	t.Run("closed reader stops draining", func(t *testing.T) {
		chunks := make(chan streamChunk, 1)
		chunks <- streamChunk{text: "ignored"}
		close(chunks)
		reader, writer := schema.Pipe[string](1)
		reader.Close()

		require.False(t, drainChunks(writer, chunks))
	})
}
