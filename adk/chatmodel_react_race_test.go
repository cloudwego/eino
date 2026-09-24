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

package adk

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// reactRaceChatModel is a minimal ToolCallingChatModel that immediately returns
// a final assistant message. It keeps the race report focused on the agent
// config mutation instead of mock-internal synchronization.
type reactRaceChatModel struct{}

func (reactRaceChatModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return reactRaceChatModel{}, nil
}

func (reactRaceChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (reactRaceChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

// reactRaceNoopTool forces the ReAct (tools) run path: the no-tools path never
// mutates the shared reactConfig, so it cannot reproduce this race.
type reactRaceNoopTool struct{}

func (reactRaceNoopTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "ping", Desc: "noop"}, nil
}

func (reactRaceNoopTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "pong", nil
}

// TestConcurrentRunSharedAgentWithTools_Race reproduces
// https://github.com/cloudwego/eino/issues/1177: the ReAct run func writes the
// per-run cancel scope into the once-built shared reactConfig, so concurrent
// Run calls on one long-lived ChatModelAgent race on reactConfig.cancelCtx and
// modelWrapperConfig.cancelContext.
func TestConcurrentRunSharedAgentWithTools_Race(t *testing.T) {
	ctx := context.Background()
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "shared",
		Description: "race repro",
		Instruction: "hi",
		Model:       reactRaceChatModel{},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{reactRaceNoopTool{}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	const runners = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			iter := agent.Run(context.Background(), &AgentInput{
				Messages:        []*schema.Message{schema.UserMessage("q")},
				EnableStreaming: false,
			})
			for {
				ev, ok := iter.Next()
				if !ok {
					return
				}
				if ev != nil && ev.Err != nil {
					t.Errorf("run err: %v", ev.Err)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
}
