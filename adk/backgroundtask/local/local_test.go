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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/schema"
)

func newTestRunner(t *testing.T, configure ...func(*Config)) (*Runner, *backgroundtask.Manager) {
	t.Helper()
	var sequence atomic.Int64
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{
		IDGen: func(context.Context, *backgroundtask.AllocateTaskIDRequest) (string, error) {
			return fmt.Sprintf("test_%d", sequence.Add(1)), nil
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
		next, err := manager.WaitUpdate(ctx, &backgroundtask.WaitUpdateRequest{
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
	assert.Equal(t, backgroundtask.StatusRunning, background.Status)
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
			config.ShouldAutoBackground = func(context.Context, *backgroundtask.Task) bool {
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
		assert.Equal(t, backgroundtask.StatusRunning, task.Status)
		task = waitTerminal(t, manager, task)
		assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	})
}

func TestRunnerStreamProjectsAndPersistsOutput_BitsUT(t *testing.T) {
	timeout := 0
	runner, manager := newTestRunner(t, func(config *Config) {
		config.ForegroundTimeoutMs = &timeout
	})
	stream, err := runner.RunStream(context.Background(), &Input{
		Description: "stream",
	}, streamWork("a", "b", "c"))
	require.NoError(t, err)
	assert.Equal(t, "abc", drain(t, stream))

	task := onlyTask(t, manager)
	task = waitTerminal(t, manager, task)
	assert.Equal(t, "abc", string(task.ResultData))
	output, err := manager.ReadOutput(context.Background(), &backgroundtask.ReadOutputRequest{
		TaskID: task.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, output.Records, 3)
	assert.Equal(t, "b", string(output.Records[1].Data))
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
	task := waitTerminal(t, manager, onlyTask(t, manager))
	assert.Equal(t, backgroundtask.StatusFailed, task.Status)
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
