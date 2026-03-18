//go:build gencheckpoints

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

package _gen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type checkpointStore struct {
	data map[string][]byte
}

func (s *checkpointStore) Set(_ context.Context, key string, value []byte) error {
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *checkpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.data[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v...), true, nil
}

type interruptTool struct {
	name string
}

func (t *interruptTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "interrupt tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"action": {Type: schema.String},
		}),
	}, nil
}

func (t *interruptTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	wasInterrupted, _, _ := tool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return "", tool.StatefulInterrupt(ctx, "needs approval", argumentsInJSON)
	}
	return "resumed", nil
}

type scriptedModel struct {
	next func() (*schema.Message, error)
}

func (m *scriptedModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.next()
}

func (m *scriptedModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream not supported")
}

func TestGenerateV084CheckpointData(t *testing.T) {
	if os.Getenv("EINO_UPDATE_CHECKPOINT_FIXTURES") != "1" {
		t.Skip("set EINO_UPDATE_CHECKPOINT_FIXTURES=1 to generate checkpoint fixtures")
	}

	ctx := context.Background()

	interruptToolName := "interrupt_in_subagent_tool"
	subTool := &interruptTool{name: interruptToolName}

	deepCalls := 0
	deepModel := &scriptedModel{
		next: func() (*schema.Message, error) {
			deepCalls++
			if deepCalls == 1 {
				c := schema.ToolCall{ID: "id-1", Type: "function"}
				c.Function.Name = "task"
				c.Function.Arguments = `{"subagent_type":"sub_chatmodel_agent","description":"from_parent"}`
				return schema.AssistantMessage("", []schema.ToolCall{c}), nil
			}
			return schema.AssistantMessage("deep done", nil), nil
		},
	}

	subCalls := 0
	subModel := &scriptedModel{
		next: func() (*schema.Message, error) {
			subCalls++
			if subCalls == 1 {
				c := schema.ToolCall{ID: "id-2", Type: "function"}
				c.Function.Name = interruptToolName
				c.Function.Arguments = `{"action":"interrupt"}`
				return schema.AssistantMessage("", []schema.ToolCall{c}), nil
			}
			return schema.AssistantMessage("sub done", nil), nil
		},
	}

	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "sub_chatmodel_agent",
		Description: "sub agent",
		Model:       subModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{subTool},
			},
		},
		MaxIterations: 4,
	})
	require.NoError(t, err)

	deepAgent, err := deep.New(ctx, &deep.Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              deepModel,
		SubAgents:              []adk.Agent{subAgent},
		MaxIteration:           4,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	require.NoError(t, err)

	store := &checkpointStore{data: map[string][]byte{}}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           deepAgent,
		CheckPointStore: store,
	})

	checkpointID := "checkpoint_gen_v0_8_4"
	it := runner.Query(ctx, "input", adk.WithCheckPointID(checkpointID))
	for {
		ev, ok := it.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	data, ok := store.data[checkpointID]
	require.True(t, ok)
	require.NotEmpty(t, data)

	outPath := filepath.Clean(filepath.Join("..", "checkpoint_data_v0.8.4.bin"))
	require.NoError(t, os.WriteFile(outPath, data, 0o644))
}
