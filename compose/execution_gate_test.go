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

package compose

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

func TestToolCallExecutionWaitsForContextGate(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, *toolCallTask)
	}{
		{
			name: "invoke",
			run: func(ctx context.Context, task *toolCallTask) {
				runToolCallTaskByInvoke(ctx, task)
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context, task *toolCallTask) {
				runToolCallTaskByStream(ctx, task)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateEntered := make(chan struct{})
			gateRelease := make(chan struct{})
			called := make(chan struct{})
			ctx := core.WithExecutionGate(
				context.Background(),
				func(ctx context.Context) error {
					close(gateEntered)
					select {
					case <-gateRelease:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
			)
			task := &toolCallTask{
				meta: &executorMeta{},
				endpoint: func(context.Context, *ToolInput) (*ToolOutput, error) {
					close(called)
					return &ToolOutput{Result: "ok"}, nil
				},
				streamEndpoint: func(context.Context, *ToolInput) (*StreamToolOutput, error) {
					close(called)
					return &StreamToolOutput{
						Result: schema.StreamReaderFromArray([]string{"ok"}),
					}, nil
				},
			}
			done := make(chan struct{})
			go func() {
				test.run(ctx, task)
				close(done)
			}()

			<-gateEntered
			select {
			case <-called:
				t.Fatal("tool call started before the execution gate opened")
			case <-time.After(10 * time.Millisecond):
			}
			close(gateRelease)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("tool call did not resume after the execution gate opened")
			}
			require.NoError(t, task.err)
			require.True(t, task.executed)
		})
	}
}

func TestToolCallExecutionStopsWhenContextGateFails(t *testing.T) {
	gateErr := errors.New("execution gate closed")
	ctx := core.WithExecutionGate(
		context.Background(),
		func(context.Context) error {
			return gateErr
		},
	)
	task := &toolCallTask{
		meta: &executorMeta{},
		endpoint: func(context.Context, *ToolInput) (*ToolOutput, error) {
			t.Fatal("tool call started after the execution gate failed")
			return nil, nil
		},
	}

	runToolCallTaskByInvoke(ctx, task)

	require.ErrorIs(t, task.err, gateErr)
	require.False(t, task.executed)
}
