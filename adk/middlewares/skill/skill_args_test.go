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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestInvokableRun_DefaultStringArgs(t *testing.T) {
	st := &typedSkillTool[*schema.Message]{
		b: &inMemoryBackend{m: []Skill{{
			FrontMatter:   FrontMatter{Name: "alpha"},
			Content:       "instructions",
			BaseDirectory: "/skills/alpha",
		}}},
	}

	result, err := st.InvokableRun(context.Background(), `{"skill":"alpha","args":"focus on PDFs"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "ARGUMENTS: focus on PDFs")
}

func TestInvokableRun_CustomNonStringArgsReachBuildContent(t *testing.T) {
	tests := []struct {
		name      string
		dataType  schema.DataType
		arguments string
	}{
		{name: "object", dataType: schema.Object, arguments: `{"skill":"alpha","args":{"path":"a.pdf"}}`},
		{name: "array", dataType: schema.Array, arguments: `{"skill":"alpha","args":["a.pdf","b.pdf"]}`},
		{name: "integer", dataType: schema.Integer, arguments: `{"skill":"alpha","args":2}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var received string
			middleware, err := NewMiddleware(context.Background(), &Config{
				Backend: &inMemoryBackend{m: []Skill{{
					FrontMatter: FrontMatter{Name: "alpha"},
					Content:     "instructions",
				}}},
				CustomToolParams: func(_ context.Context, defaults map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error) {
					defaults["args"] = &schema.ParameterInfo{Type: tt.dataType}
					return defaults, nil
				},
				BuildContent: func(_ context.Context, _ Skill, rawArgs string) (string, error) {
					received = rawArgs
					return "custom content", nil
				},
			})
			require.NoError(t, err)
			st := middleware.(*typedSkillHandler[*schema.Message]).tool

			info, err := st.Info(context.Background())
			require.NoError(t, err)
			jsonSchema, err := info.ParamsOneOf.ToJSONSchema()
			require.NoError(t, err)
			argsSchema, ok := jsonSchema.Properties.Get("args")
			require.True(t, ok)
			assert.Equal(t, string(tt.dataType), argsSchema.Type)

			result, err := st.InvokableRun(context.Background(), tt.arguments)
			require.NoError(t, err)
			assert.Equal(t, "custom content", result)
			assert.JSONEq(t, tt.arguments, received)
		})
	}
}

func TestInvokableRun_CustomObjectArgsReachForkExtensions(t *testing.T) {
	for _, mode := range []ContextMode{ContextModeFork, ContextModeForkWithContext} {
		t.Run(string(mode), func(t *testing.T) {
			const arguments = `{"skill":"alpha","args":{"path":"a.pdf"}}`
			agent := &mockAgent{events: []*adk.AgentEvent{{
				Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
					Message: schema.AssistantMessage("done", nil),
				}},
			}}}
			var skillContent, buildMessagesArgs, formatResultArgs string
			st := &typedSkillTool[*schema.Message]{
				b: &inMemoryBackend{m: []Skill{{
					FrontMatter: FrontMatter{Name: "alpha", Context: mode},
					Content:     "instructions",
				}}},
				agentHub: &mockAgentHub{defaultAgent: agent},
				buildForkMessages: func(_ context.Context, in SubAgentInput) ([]adk.Message, error) {
					skillContent = in.SkillContent
					buildMessagesArgs = in.RawArguments
					return []adk.Message{schema.UserMessage(in.SkillContent)}, nil
				},
				formatForkResult: func(_ context.Context, in SubAgentOutput) (string, error) {
					formatResultArgs = in.RawArguments
					return "formatted", nil
				},
			}

			invoke := func(ctx context.Context) {
				result, err := st.InvokableRun(ctx, arguments)
				require.NoError(t, err)
				assert.Equal(t, "formatted", result)
			}
			if mode == ContextModeForkWithContext {
				withRunLocalCtx(t, invoke)
			} else {
				invoke(context.Background())
			}

			assert.Contains(t, skillContent, `{"path":"a.pdf"}`)
			assert.JSONEq(t, arguments, buildMessagesArgs)
			assert.JSONEq(t, arguments, formatResultArgs)
		})
	}
}
