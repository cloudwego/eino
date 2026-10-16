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

package skill

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type recoveryLoopModel[M adk.MessageType] struct {
	mu            sync.Mutex
	arguments     []string
	calls         int
	generateCalls int
	streamCalls   int
	feedback      []string
}

func recoveryMessage[M adk.MessageType](callID, args string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		if callID == "" {
			return any(schema.AssistantMessage("DONE", nil)).(M)
		}
		return any(schema.AssistantMessage("", []schema.ToolCall{{
			ID: callID, Type: "function", Function: schema.FunctionCall{Name: "skill", Arguments: args},
		}})).(M)
	case *schema.AgenticMessage:
		block := schema.NewContentBlock(&schema.AssistantGenText{Text: "DONE"})
		if callID != "" {
			block = schema.NewContentBlock(&schema.FunctionToolCall{Name: "skill", CallID: callID, Arguments: args})
		}
		return any(&schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: []*schema.ContentBlock{block}}).(M)
	}
	panic("unsupported message type")
}

func recoveryMessageOutput[M adk.MessageType](msg M) (text, callID string, final bool) {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Content, m.ToolCallID, m.Role == schema.Assistant && len(m.ToolCalls) == 0
	case *schema.AgenticMessage:
		for _, block := range m.ContentBlocks {
			if result := block.FunctionToolResult; result != nil {
				for _, part := range result.Content {
					if part.Text != nil {
						text += part.Text.Text
					}
				}
				return text, result.CallID, false
			}
			if block.AssistantGenText != nil {
				return block.AssistantGenText.Text, "", true
			}
		}
	}
	return "", "", false
}

func (m *recoveryLoopModel[M]) next(input []M, streaming bool) M {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if streaming {
		m.streamCalls++
	} else {
		m.generateCalls++
	}
	if len(input) > 0 {
		text, callID, _ := recoveryMessageOutput(input[len(input)-1])
		if callID != "" {
			m.feedback = append(m.feedback, text)
		}
	}
	if m.calls <= len(m.arguments) {
		return recoveryMessage[M](fmt.Sprintf("call_%d", m.calls), m.arguments[m.calls-1])
	}
	return recoveryMessage[M]("", "")
}

func (m *recoveryLoopModel[M]) Generate(_ context.Context, input []M, _ ...model.Option) (M, error) {
	return m.next(input, false), nil
}

func (m *recoveryLoopModel[M]) Stream(_ context.Context, input []M, _ ...model.Option) (*schema.StreamReader[M], error) {
	msg := m.next(input, true)
	if message, ok := any(msg).(*schema.Message); ok && len(message.ToolCalls) > 0 {
		call := message.ToolCalls[0]
		index := 0
		call.Index = &index
		split := len(call.Function.Arguments) / 2
		remaining := call.Function.Arguments[split:]
		call.Function.Arguments = call.Function.Arguments[:split]
		return schema.StreamReaderFromArray([]M{
			any(schema.AssistantMessage("", []schema.ToolCall{call})).(M),
			any(schema.AssistantMessage("", []schema.ToolCall{{Index: &index, Function: schema.FunctionCall{Arguments: remaining}}})).(M),
		}), nil
	}
	return schema.StreamReaderFromArray([]M{msg}), nil
}

func newRecoveryLoopAgent[M adk.MessageType](t *testing.T, registration string, backend Backend,
	m *recoveryLoopModel[M], enabled bool) *adk.TypedChatModelAgent[M] {
	t.Helper()
	cfg := &adk.TypedChatModelAgentConfig[M]{Name: "skill_recovery", Model: m, MaxIterations: 4}
	if registration == "legacy" {
		mw, err := New(context.Background(), &Config{Backend: backend, RecoverInputErrors: enabled})
		require.NoError(t, err)
		cfg.Middlewares = []adk.AgentMiddleware{mw}
	} else {
		h, err := NewTyped(context.Background(), &TypedConfig[M]{Backend: backend, RecoverInputErrors: enabled})
		require.NoError(t, err)
		cfg.Handlers = []adk.TypedChatModelAgentMiddleware[M]{h}
	}
	a, err := adk.NewTypedChatModelAgent(context.Background(), cfg)
	require.NoError(t, err)
	return a
}

type recoveryRunResult struct {
	errors      []error
	toolResults []string
	callIDs     []string
	final       string
	interrupts  int
}

func collectRecoveryRun[M adk.MessageType](t *testing.T, iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) recoveryRunResult {
	t.Helper()
	var result recoveryRunResult
	for {
		event, ok := iter.Next()
		if !ok {
			return result
		}
		if event.Err != nil {
			result.errors = append(result.errors, event.Err)
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			result.interrupts++
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, err := event.Output.MessageOutput.GetMessage()
		require.NoError(t, err)
		text, callID, final := recoveryMessageOutput(msg)
		if callID != "" {
			result.callIDs = append(result.callIDs, callID)
			result.toolResults = append(result.toolResults, text)
		}
		if final {
			result.final = text
		}
	}
}

func testRecoveryAgentLoop[M adk.MessageType](t *testing.T, registration string) {
	cases := []struct {
		name     string
		args     string
		getErr   error
		inputErr bool
	}{
		{name: "missing", args: `{"skill":"missing"}`, inputErr: true},
		{name: "invalid_json", args: `{`, inputErr: true},
		{name: "invalid_type", args: `{"skill":3}`, inputErr: true},
		{name: "backend_failure", args: `{"skill":"good"}`, getErr: errRecoveryBackendUnavailable},
		{name: "valid", args: `{"skill":"good"}`},
	}
	for _, streaming := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/stream=%t/recover=%t", tc.name, streaming, enabled), func(t *testing.T) {
					backend := Backend(&recoveryBackend{})
					if tc.getErr != nil {
						backend = &errorBackend{getErr: tc.getErr}
					}
					m := &recoveryLoopModel[M]{arguments: []string{tc.args, `{"skill":"good"}`}}
					a := newRecoveryLoopAgent(t, registration, backend, m, enabled)
					runner := adk.NewTypedRunner(adk.TypedRunnerConfig[M]{Agent: a, EnableStreaming: streaming})
					result := collectRecoveryRun(t, runner.Query(context.Background(), "Use a skill"))
					if tc.getErr != nil || tc.inputErr && !enabled {
						require.Len(t, result.errors, 1)
						require.Equal(t, 1, m.calls)
						require.Empty(t, result.final)
						require.Empty(t, result.toolResults)
						if tc.getErr != nil {
							require.ErrorIs(t, result.errors[0], tc.getErr)
						}
					} else {
						require.Empty(t, result.errors)
						require.Equal(t, 3, m.calls)
						require.Equal(t, "DONE", result.final)
						require.Equal(t, []string{"call_1", "call_2"}, result.callIDs)
						require.Len(t, m.feedback, 2)
						require.Equal(t, result.toolResults, m.feedback)
						require.Contains(t, m.feedback[1], "valid skill content")
						if tc.inputErr {
							require.Contains(t, m.feedback[0], "[skill error]")
							require.Contains(t, m.feedback[0], "Check the skill name and arguments, then retry.")
						}
					}
					require.Zero(t, result.interrupts)
					if streaming {
						require.Zero(t, m.generateCalls)
						require.Equal(t, m.calls, m.streamCalls)
					} else {
						require.Zero(t, m.streamCalls)
						require.Equal(t, m.calls, m.generateCalls)
					}
				})
			}
		}
	}
}

func TestSkillRecoveryAgentLoop(t *testing.T) {
	t.Run("legacy", func(t *testing.T) { testRecoveryAgentLoop[*schema.Message](t, "legacy") })
	t.Run("typed_message", func(t *testing.T) { testRecoveryAgentLoop[*schema.Message](t, "typed") })
	t.Run("typed_agentic", func(t *testing.T) { testRecoveryAgentLoop[*schema.AgenticMessage](t, "typed") })
}

func TestSkillRecoveryInterruptResume(t *testing.T) {
	for _, registration := range []string{"legacy", "typed"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", registration, streaming), func(t *testing.T) {
				backend := &recoveryBackend{get: func(ctx context.Context, _ string) (Skill, error) {
					if interrupted, _, _ := tool.GetInterruptState[any](ctx); !interrupted {
						return Skill{}, notFoundControlError{cause: tool.Interrupt(ctx, "approval required")}
					}
					return Skill{Content: "approved skill"}, nil
				}}
				m := &recoveryLoopModel[*schema.Message]{arguments: []string{`{"skill":"good"}`}}
				a := newRecoveryLoopAgent(t, registration, backend, m, true)
				store := &recoveryCheckpointStore{data: make(map[string][]byte)}
				runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: a, EnableStreaming: streaming, CheckPointStore: store})
				result := collectRecoveryRun(t, runner.Query(context.Background(), "Use a skill", adk.WithCheckPointID("approval")))
				require.Empty(t, result.errors)
				require.Equal(t, 1, result.interrupts)
				require.Equal(t, 1, m.calls)
				iter, err := runner.Resume(context.Background(), "approval")
				require.NoError(t, err)
				resumed := collectRecoveryRun(t, iter)
				require.Empty(t, resumed.errors)
				require.Zero(t, resumed.interrupts)
				require.Equal(t, "DONE", resumed.final)
				require.Equal(t, 2, m.calls)
				require.Len(t, resumed.toolResults, 1)
				require.Contains(t, resumed.toolResults[0], "approved skill")
			})
		}
	}
}
