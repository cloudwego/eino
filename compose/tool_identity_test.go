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
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type identityResumeTool struct {
	mu        sync.Mutex
	calls     map[string]int
	ids       map[string]string
	addresses map[string]string
	legacy    bool
}

func (m *identityResumeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "identity"}, nil
}

func (m *identityResumeTool) InvokableRun(ctx context.Context, arg string, _ ...tool.Option) (string, error) {
	m.mu.Lock()
	m.calls[arg]++
	m.ids[arg] = GetToolCallID(ctx)
	m.addresses[arg] = GetCurrentAddress(ctx).String()
	m.mu.Unlock()
	if arg == "done" {
		return "done", nil
	}
	if target, hasData, data := tool.GetResumeContext[string](ctx); target && hasData {
		return arg + ":" + data, nil
	}
	if m.legacy {
		return "", NewInterruptAndRerunErr(arg)
	}
	if interrupted, hasState, state := tool.GetInterruptState[string](ctx); interrupted && (!hasState || state != arg) {
		return "", fmt.Errorf("wrong checkpoint state for %s: %q", arg, state)
	}
	return "", tool.StatefulInterrupt(ctx, arg, arg)
}

func (m *identityResumeTool) StreamableRun(ctx context.Context, arg string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	value, err := m.InvokableRun(ctx, arg, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]string{value}), nil
}

func TestToolIdentitySelectiveResume(t *testing.T) {
	for name, ids := range map[string][]string{
		"empty":               {"", "", ""},
		"duplicate":           {"same", "same", "same"},
		"generated_collision": {"__eino_tool_call_1", "same", "same"},
		"unique":              {"a", "b", "c"},
	} {
		for _, stream := range []bool{false, true} {
			for _, sequential := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/stream=%v/sequential=%v", name, stream, sequential), func(t *testing.T) {
					ctx := context.Background()
					impl := &identityResumeTool{calls: map[string]int{}, ids: map[string]string{}, addresses: map[string]string{}}
					node, err := NewToolNode(ctx, &ToolsNodeConfig{Tools: []tool.BaseTool{impl}, ExecuteSequentially: sequential})
					require.NoError(t, err)
					g := NewGraph[*schema.Message, []*schema.Message]()
					require.NoError(t, g.AddToolsNode("tools", node))
					require.NoError(t, g.AddEdge(START, "tools"))
					require.NoError(t, g.AddEdge("tools", END))
					graph, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
					require.NoError(t, err)
					input := schema.AssistantMessage("", nil)
					for i, arg := range []string{"done", "first", "second"} {
						input.ToolCalls = append(input.ToolCalls, schema.ToolCall{ID: ids[i], Function: schema.FunctionCall{Name: "identity", Arguments: arg}})
					}
					run := func(ctx context.Context, input *schema.Message) ([]*schema.Message, error) {
						if !stream {
							return graph.Invoke(ctx, input, WithCheckPointID("identity"))
						}
						reader, err := graph.Stream(ctx, input, WithCheckPointID("identity"))
						if err != nil {
							return nil, err
						}
						return concatStreamReader(reader)
					}
					_, err = run(ctx, input)
					info, ok := ExtractInterruptInfo(err)
					require.True(t, ok, "%v", err)
					require.Len(t, info.InterruptContexts, 2)
					targets := map[string]*InterruptCtx{}
					for _, interrupt := range info.InterruptContexts {
						targets[interrupt.Info.(string)] = interrupt
					}
					require.Contains(t, targets, "first")
					require.Contains(t, targets, "second")
					require.NotEqual(t, targets["first"].Address.String(), targets["second"].Address.String())
					_, err = run(ResumeWithData(ctx, targets["first"].ID, "one"), nil)
					info, ok = ExtractInterruptInfo(err)
					require.True(t, ok, "%v", err)
					require.Len(t, info.InterruptContexts, 1)
					require.Equal(t, "second", info.InterruptContexts[0].Info)
					require.Equal(t, targets["second"].Address, info.InterruptContexts[0].Address)
					output, err := run(ResumeWithData(ctx, info.InterruptContexts[0].ID, "two"), nil)
					require.NoError(t, err)
					require.Len(t, output, 3)
					for i, want := range []string{"done", "first:one", "second:two"} {
						require.Equal(t, want, output[i].Content)
						require.Equal(t, ids[i], output[i].ToolCallID)
					}
					require.Equal(t, map[string]int{"done": 1, "first": 2, "second": 3}, impl.calls)
					require.Equal(t, map[string]string{"done": ids[0], "first": ids[1], "second": ids[2]}, impl.ids)
				})
			}
		}
	}
}

func TestToolIdentityLegacyAddresses(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			impl := &identityResumeTool{calls: map[string]int{}, ids: map[string]string{}, addresses: map[string]string{}, legacy: true}
			node, err := NewToolNode(context.Background(), &ToolsNodeConfig{Tools: []tool.BaseTool{impl}})
			require.NoError(t, err)
			input := schema.AssistantMessage("", []schema.ToolCall{
				{ID: "same", Function: schema.FunctionCall{Name: "identity", Arguments: "first"}},
				{ID: "same", Function: schema.FunctionCall{Name: "identity", Arguments: "second"}},
			})

			g := NewGraph[*schema.Message, []*schema.Message]()
			require.NoError(t, g.AddToolsNode("tools", node))
			require.NoError(t, g.AddEdge(START, "tools"))
			require.NoError(t, g.AddEdge("tools", END))
			graph, err := g.Compile(context.Background(), WithCheckPointStore(newInMemoryStore()))
			require.NoError(t, err)
			if stream {
				var reader *schema.StreamReader[[]*schema.Message]
				reader, err = graph.Stream(context.Background(), input, WithCheckPointID("legacy"))
				if err == nil {
					_, err = concatStreamReader(reader)
				}
			} else {
				_, err = graph.Invoke(context.Background(), input, WithCheckPointID("legacy"))
			}
			info, ok := ExtractInterruptInfo(err)
			require.True(t, ok, "%v", err)
			require.Len(t, info.InterruptContexts, 2)
			for _, interrupt := range info.InterruptContexts {
				require.Equal(t, impl.addresses[interrupt.Info.(string)], interrupt.Address.String())
			}
			require.NotEqual(t, info.InterruptContexts[0].Address, info.InterruptContexts[1].Address)
		})
	}
}

func TestToolIdentityCheckpointCompatibility(t *testing.T) {
	for name, ids := range map[string][]string{"unique": {"a", "b"}, "empty": {"", ""}, "duplicate": {"same", "same"}} {
		t.Run(name, func(t *testing.T) {
			input := schema.AssistantMessage("", nil)
			for _, id := range ids {
				input.ToolCalls = append(input.ToolCalls, schema.ToolCall{ID: id})
			}
			state := &toolsInterruptAndRerunState{Input: input}
			if name == "unique" {
				require.NoError(t, validateToolCheckpointKeys(state))
			} else {
				require.ErrorContains(t, validateToolCheckpointKeys(state), "cannot resume legacy tool checkpoint")
			}
			state.ToolKeyVersion = 1
			require.NoError(t, validateToolCheckpointKeys(state))
		})
	}
}

func TestToolIdentityStreamCheckpointMaps(t *testing.T) {
	input := schema.AssistantMessage("", []schema.ToolCall{
		{ID: ""}, {ID: "same"}, {ID: "same"}, {ID: "__eino_tool_call_0"},
	})
	keys := toolCallKeys(input.ToolCalls)
	require.Equal(t, "__eino_tool_call_0", keys[3])
	tracker := newToolStreamCheckpointTracker(input)
	tracker.recordTool(keys[0], "completed")
	enhanced := &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "enhanced"}}}
	tracker.recordEnhancedTool(keys[1], enhanced)
	tracker.recordRerun(keys[2])
	tracker.recordTool(keys[3], "unique")
	var checkpoint *toolStreamCheckpointError
	require.ErrorAs(t, tracker.interrupt(context.Background()), &checkpoint)
	state := checkpoint.signal.State.(*toolsInterruptAndRerunState)
	require.Equal(t, map[string]string{keys[0]: "completed", keys[3]: "unique"}, state.ExecutedTools)
	require.Equal(t, map[string]*schema.ToolResult{keys[1]: enhanced}, state.ExecutedEnhancedTools)
	require.Equal(t, []string{keys[2]}, state.RerunTools)
	require.Equal(t, 1, state.ToolKeyVersion)
	extra := checkpoint.signal.Info.(*ToolsInterruptAndRerunExtra)
	require.Equal(t, keys, extra.ToolCallKeys)
}
