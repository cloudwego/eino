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

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/foreground"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *backgroundtask.Config,
) *backgroundtask.Manager {
	t.Helper()
	if config == nil {
		config = &backgroundtask.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *backgroundtask.Task) error { return nil }
	}
	manager, err := backgroundtask.New(ctx, config)
	require.NoError(t, err)
	return manager
}

type countingGetStore struct {
	*backgroundtask.InMemoryStore
	getCount int64
}

func (s *countingGetStore) Get(
	ctx context.Context,
	taskID string,
) (*backgroundtask.Task, error) {
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

func newTestRunner(t *testing.T, configure ...func(*Config)) (*Runner, *backgroundtask.Manager) {
	t.Helper()
	var sequence int64
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
		Executors: executors,
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return fmt.Sprintf("test_%d", atomic.AddInt64(&sequence, 1)), nil
		},
	})
	config := &Config{Manager: manager, Executors: executors}
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

func waitTerminal(
	t *testing.T,
	manager *backgroundtask.Manager,
	task *backgroundtask.Task,
) *backgroundtask.Task {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for task.Status == backgroundtask.StatusPending ||
		task.Status == backgroundtask.StatusRunning {
		next, err := manager.WaitForTaskVersion(ctx, &backgroundtask.WaitForTaskVersionRequest{
			TaskID: task.Spec.ID, AfterVersion: task.Version,
		})
		require.NoError(t, err)
		task = next
	}
	return task
}

func TestRunnerBufferedForegroundAndBackground_BitsUT(t *testing.T) {
	runner, manager := newTestRunner(t)
	foreground, err := runner.Run(context.Background(), &Input{Description: "foreground"},
		func(context.Context, backgroundtask.ExecutionRuntime) (string, error) {
			return "done", nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusCompleted, foreground.Status)
	assert.Equal(t, "done", string(foreground.ResultData))

	background, err := runner.Run(context.Background(), &Input{
		Description: "background", RunInBackground: true,
	}, func(context.Context, backgroundtask.ExecutionRuntime) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return "later", nil
	})
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusPending, background.Status)
	background = waitTerminal(t, manager, background)
	assert.Equal(t, backgroundtask.StatusCompleted, background.Status)
	assert.Equal(t, "later", string(background.ResultData))
}

func TestRunnerForegroundTimeoutPolicies_BitsUT(t *testing.T) {
	t.Run("fail", func(t *testing.T) {
		timeout := 10
		runner, _ := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
		})
		task, err := runner.Run(context.Background(), &Input{Description: "timeout"},
			func(ctx context.Context, _ backgroundtask.ExecutionRuntime) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
		)
		require.NoError(t, err)
		assert.Equal(t, backgroundtask.StatusFailed, task.Status)
		assert.Equal(t, "timed out after 10ms", task.ResultError)
	})

	t.Run("auto background", func(t *testing.T) {
		timeout := 10
		runner, manager := newTestRunner(t, func(config *Config) {
			config.ForegroundTimeoutMs = &timeout
			config.ShouldAutoBackground = func(context.Context, *foreground.CandidateInfo) bool {
				return true
			}
		})
		task, err := runner.Run(context.Background(), &Input{Description: "background"},
			func(context.Context, backgroundtask.ExecutionRuntime) (string, error) {
				time.Sleep(30 * time.Millisecond)
				return "done", nil
			},
		)
		require.NoError(t, err)
		assert.Equal(t, backgroundtask.StatusPending, task.Status)
		task = waitTerminal(t, manager, task)
		assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	})
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

	_, err = manager.Get(context.Background(), "test_1")
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)
}

func TestRunnerStreamTimeoutStartsAfterConstruction_BitsUT(t *testing.T) {
	timeout := 10
	runner, _ := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
	})
	stream, err := runner.RunStream(context.Background(), &Input{Description: "construct"},
		func(context.Context, backgroundtask.ExecutionRuntime) (*schema.StreamReader[string], error) {
			time.Sleep(20 * time.Millisecond)
			return streamWork("done")(context.Background(), nil)
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "done", drain(t, stream))
}

func TestRunnerStreamErrorFailsProjectionAndTask_BitsUT(t *testing.T) {
	runner, manager := newTestRunner(t)
	wantErr := errors.New("boom")
	stream, err := runner.RunStream(context.Background(), &Input{Description: "stream"},
		func(context.Context, backgroundtask.ExecutionRuntime) (*schema.StreamReader[string], error) {
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
	_, err = manager.Get(context.Background(), "test_1")
	require.ErrorIs(t, err, backgroundtask.ErrNotFound)
}

func TestRunnerStreamConstructionFailures(t *testing.T) {
	runner, _ := newTestRunner(t)
	wantErr := errors.New("construct failed")
	cases := []struct {
		name string
		work StreamWorkFunc
	}{
		{name: "work error", work: func(
			context.Context, backgroundtask.ExecutionRuntime,
		) (*schema.StreamReader[string], error) {
			return nil, wantErr
		}},
		{name: "nil reader", work: func(
			context.Context, backgroundtask.ExecutionRuntime,
		) (*schema.StreamReader[string], error) {
			return nil, nil
		}},
		{name: "panic", work: func(
			context.Context, backgroundtask.ExecutionRuntime,
		) (*schema.StreamReader[string], error) {
			panic(wantErr)
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stream, err := runner.RunStream(
				context.Background(), &Input{Description: testCase.name}, testCase.work,
			)
			require.Error(t, err)
			require.Nil(t, stream)
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
				backgroundtask.ExecutionRuntime,
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
			InMemoryStore: backgroundtask.NewInMemoryStore(nil),
		}
		manager := mustNewBackgroundManager(t, context.Background(), &backgroundtask.Config{
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
	newRunner := func(t *testing.T, auto bool) (*Runner, *backgroundtask.Manager) {
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
		require.Equal(t, backgroundtask.StatusCompleted,
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
		require.Equal(t, backgroundtask.StatusCompleted,
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
		require.Equal(t, backgroundtask.StatusCompleted,
			waitTerminal(t, manager, onlyTask(t, manager)).Status)
	})
}

func TestRunnerLocalContracts(t *testing.T) {
	runner, manager := newTestRunner(t)
	require.Same(t, manager, runner.Manager())
	var nilRunner *Runner
	require.Nil(t, nilRunner.Manager())
	require.Error(t, runner.executor.ValidateSpec(backgroundtask.Spec{
		ExecutorKey: "wrong",
	}))

	task := &backgroundtask.Task{Spec: backgroundtask.Spec{
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

	work := func(context.Context, backgroundtask.ExecutionRuntime) (string, error) {
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
	return func(context.Context, backgroundtask.ExecutionRuntime) (*schema.StreamReader[string], error) {
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
	return func(context.Context, backgroundtask.ExecutionRuntime) (*schema.StreamReader[string], error) {
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
	defer reader.Close()
	var result strings.Builder
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			return result.String()
		}
		require.NoError(t, err)
		result.WriteString(chunk)
	}
}

func onlyTask(t *testing.T, manager *backgroundtask.Manager) *backgroundtask.Task {
	t.Helper()
	task, err := manager.Get(context.Background(), "test_1")
	require.NoError(t, err)
	return task
}
