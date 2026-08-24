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

package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type directSubmitAdapter struct {
	manager  *background.Manager
	registry *Registry
	task     *background.TaskSnapshot
}

func (*directSubmitAdapter) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("submit_direct"), nil
}

func (a *directSubmitAdapter) InvokableRun(
	ctx context.Context,
	arguments string,
	_ ...componenttool.Option,
) (string, error) {
	task, err := Submit(ctx, a.manager, a.registry, &SubmitRequest{
		TaskID: "tool-call-task", ToolName: "external",
		Arguments: arguments, SessionID: "parent",
	})
	if err != nil {
		return "", err
	}
	a.task = task
	return task.Spec.ID, nil
}

func TestAttack_DirectSubmitPreservesToolCallIDFromToolNode(t *testing.T) {
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"),
		Tool: &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(context.Context) (*Outcome, error) {
						return &Outcome{Status: task.OutcomeCompleted}, nil
					},
				}, nil
			},
		},
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{})
	adapter := &directSubmitAdapter{manager: manager, registry: registry}
	node, err := compose.NewToolNode(context.Background(), &compose.ToolsNodeConfig{
		Tools: []componenttool.BaseTool{adapter},
	})
	require.NoError(t, err)
	const callID = "call_direct_123"
	_, err = node.Invoke(context.Background(), &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: callID,
			Function: schema.FunctionCall{
				Name: "submit_direct", Arguments: `{"value":"input"}`,
			},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, adapter.task)
	var payload taskPayload
	require.NoError(t, json.Unmarshal(adapter.task.Spec.Payload, &payload))
	t.Logf("private payload captured ToolCallID %q", payload.ToolCallID)
	require.Equal(t, callID, payload.ToolCallID)
}

func TestAttack_CompletedOutcomeDataRemainsByteIdentical(t *testing.T) {
	source := []byte{0x00, 0xff, 0x80, '{', '}', '\n'}
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("binary"),
		Tool: &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(context.Context) (*Outcome, error) {
						return &Outcome{
							Status: task.OutcomeCompleted,
							Data:   source,
						}, nil
					},
				}, nil
			},
		},
	}))
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{})
	task, err := Submit(context.Background(), manager, registry, &SubmitRequest{
		TaskID: "binary-task", ToolName: "binary",
		Arguments: "{}", SessionID: "parent",
	})
	require.NoError(t, err)
	expected := append([]byte(nil), source...)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	source[0] = 'X'
	completed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	t.Logf("persisted result bytes: %x", completed.ResultData)
	require.Equal(t, expected, completed.ResultData)
}
