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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// TestAttack_FormatHintMatchesTaskEventPersister verifies that only the
// built-in JSONL serializer advertises JSONL output. A custom transcript
// formatter or TaskEventPersister owns its wire format and must not inherit a
// misleading default hint.
func TestAttack_FormatHintMatchesTaskEventPersister(t *testing.T) {
	tests := []struct {
		name      string
		format    TranscriptFormat[*schema.Message]
		persister background.TaskEventPersister[*adk.AgentEvent, *schema.Message]
		wantJSONL bool
	}{
		{
			name:      "default serializer",
			wantJSONL: true,
		},
		{
			name: "custom transcript format",
			format: func(
				context.Context,
				string,
				*schema.Message,
			) (string, error) {
				return "custom", nil
			},
		},
		{
			name:      "custom task event persister",
			persister: &capturingAgentEventPersister{},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := runnerEnvironmentContext(t)
			manager := newTestManager(t, ctx)
			release := make(chan struct{})
			agent := &mockAgent{
				name: "worker",
				run: func(context.Context, *adk.AgentInput) string {
					<-release
					return "done"
				},
			}
			middleware, err := New(ctx, &Config{
				SubAgents: []adk.Agent{agent},
				Tasks: &TaskConfig{
					TranscriptFormat: testCase.format,
					Local: &LocalTaskConfig{
						Runner:         mustLocalRunner(t, manager),
						OutputStore:    filesystem.NewInMemoryBackend(),
						OutputDir:      "/tasks",
						EventPersister: testCase.persister,
					},
				},
			})
			require.NoError(t, err)
			_, runCtx, err := middleware.BeforeAgent(
				ctx,
				&adk.ChatModelAgentContext[*schema.Message]{},
			)
			require.NoError(t, err)
			require.Len(t, runCtx.Tools, 1)

			result, err := runCtx.Tools[0].(tool.InvokableTool).InvokableRun(
				ctx,
				`{"subagent_type":"worker","prompt":"work","description":"format attack","run_in_background":true}`,
			)
			require.NoError(t, err)
			require.Contains(t, result, "running in background")
			require.Equal(t, testCase.wantJSONL, containsJSONLHint(result))

			close(release)
			completed := terminalTask(t, manager)
			require.NotNil(t, completed)
			require.Equal(t, background.StatusCompleted, completed.Status)
		})
	}
}

func containsJSONLHint(result string) bool {
	return len(result) >= len("JSONL") &&
		func() bool {
			for i := 0; i+len("JSONL") <= len(result); i++ {
				if result[i:i+len("JSONL")] == "JSONL" {
					return true
				}
			}
			return false
		}()
}
