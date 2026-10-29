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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestToolDescriptionGeneratorReturnsFinalText(t *testing.T) {
	const customDescription = `Call task with {"prompt":"full request"}; preserve {background_prompt} and {tool_name}.`
	agent := &mockAgent{name: "worker", desc: "does work"}

	assertDescription := func(t *testing.T, background *BackgroundConfig) {
		t.Helper()
		mw, err := New(context.Background(), &Config{
			SubAgents: []adk.Agent{agent},
			ToolName:  "task",
			ToolDescriptionGenerator: func(context.Context, []adk.Agent) (string, error) {
				return customDescription, nil
			},
			Background: background,
		})
		require.NoError(t, err)

		_, runCtx, err := mw.BeforeAgent(
			context.Background(),
			&adk.TypedChatModelAgentContext[*schema.Message]{},
		)
		require.NoError(t, err)
		require.Len(t, runCtx.Tools, 1)
		info, err := runCtx.Tools[0].Info(context.Background())
		require.NoError(t, err)
		assert.Equal(t, customDescription, info.Desc)
	}

	t.Run("foreground", func(t *testing.T) {
		assertDescription(t, nil)
	})

	t.Run("background", func(t *testing.T) {
		ctx := context.Background()
		manager := newTestManager(t, ctx)
		defer func() {
			require.NoError(t, manager.Close(ctx))
		}()
		assertDescription(t, localBackground(t, manager))
	})
}

func TestToolNameRendersBuiltInModelText(t *testing.T) {
	ctx := context.Background()
	manager := newTestManager(t, ctx)
	defer func() {
		require.NoError(t, manager.Close(ctx))
	}()

	mw, err := New(ctx, &Config{
		SubAgents:  []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
		ToolName:   "task",
		Background: localBackground(t, manager),
	})
	require.NoError(t, err)

	typedMW := mw.(*typedSubagentMiddleware[*schema.Message])
	_, runCtx, err := mw.BeforeAgent(ctx, &adk.TypedChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	require.Len(t, runCtx.Tools, 1)
	info, err := runCtx.Tools[0].Info(ctx)
	require.NoError(t, err)

	assert.Equal(t, "task", info.Name)
	assert.Contains(t, runCtx.Instruction, "# task tool")
	assert.Contains(t, runCtx.Instruction, "You can use the 'task' tool")
	assert.Contains(t, runCtx.Instruction, "When to use the task tool")
	assert.NotContains(t, runCtx.Instruction, "'agent' tool")

	assert.Contains(t, info.Desc, "When using the task tool")
	assert.Contains(t, info.Desc, "later background task call")
	assert.NotContains(t, info.Desc, "When using the Agent tool")
	assert.NotContains(t, info.Desc, "background agent call")

	assert.Contains(t, typedMW.reminder, "Available agent types for the task tool:")
	assert.NotContains(t, typedMW.reminder, "Available agent types for the Agent tool:")
}

func TestDefaultToolNameRendersBuiltInModelText(t *testing.T) {
	ctx := context.Background()
	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
	})
	require.NoError(t, err)

	typedMW := mw.(*typedSubagentMiddleware[*schema.Message])
	_, runCtx, err := mw.BeforeAgent(ctx, &adk.TypedChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	info, err := runCtx.Tools[0].Info(ctx)
	require.NoError(t, err)

	assert.Equal(t, agentToolName, info.Name)
	assert.Contains(t, runCtx.Instruction, "You can use the 'agent' tool")
	assert.Contains(t, info.Desc, "When using the agent tool")
	assert.Contains(t, typedMW.reminder, "Available agent types for the agent tool:")
	assert.NotContains(t, runCtx.Instruction, "{tool_name}")
	assert.NotContains(t, info.Desc, "{tool_name}")
	assert.NotContains(t, info.Desc, "{background_prompt}")
	assert.NotContains(t, typedMW.reminder, "{tool_name}")
}

func TestCustomModelTextIsUsedVerbatim(t *testing.T) {
	const customText = `Keep {tool_name} and {"name":"agent"} literal.`
	mw, err := New(context.Background(), &Config{
		SubAgents:    []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
		ToolName:     "task",
		SystemPrompt: stringPtr(customText),
		CustomFormatReminder: func(context.Context, *FormatReminderInput[*schema.Message]) (*FormatReminderOutput, error) {
			return &FormatReminderOutput{Reminder: customText}, nil
		},
	})
	require.NoError(t, err)

	typedMW := mw.(*typedSubagentMiddleware[*schema.Message])
	_, runCtx, err := mw.BeforeAgent(
		context.Background(),
		&adk.TypedChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, customText)
	assert.Equal(t, customText, typedMW.reminder)
}

func TestToolNameRendersBuiltInModelTextChinese(t *testing.T) {
	require.NoError(t, adk.SetLanguage(adk.LanguageChinese))
	t.Cleanup(func() {
		require.NoError(t, adk.SetLanguage(adk.LanguageEnglish))
	})

	mw, err := New(context.Background(), &Config{
		SubAgents: []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
		ToolName:  "task",
	})
	require.NoError(t, err)

	typedMW := mw.(*typedSubagentMiddleware[*schema.Message])
	_, runCtx, err := mw.BeforeAgent(
		context.Background(),
		&adk.TypedChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	info, err := runCtx.Tools[0].Info(context.Background())
	require.NoError(t, err)

	assert.Contains(t, runCtx.Instruction, "# task 工具")
	assert.Contains(t, runCtx.Instruction, "你可以使用 'task' 工具")
	assert.Contains(t, info.Desc, "使用 task 工具时")
	assert.Contains(t, typedMW.reminder, "task 工具可用的智能体类型：")
}

func TestToolNameRendersAgenticModelText(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &TypedConfig[*schema.AgenticMessage]{
		SubAgents: []adk.TypedAgent[*schema.AgenticMessage]{
			agenticMockAgent{name: "worker", desc: "does work"},
		},
		ToolName: "task",
	})
	require.NoError(t, err)

	typedMW := mw.(*typedSubagentMiddleware[*schema.AgenticMessage])
	_, runCtx, err := mw.BeforeAgent(
		ctx,
		&adk.TypedChatModelAgentContext[*schema.AgenticMessage]{},
	)
	require.NoError(t, err)
	info, err := runCtx.Tools[0].Info(ctx)
	require.NoError(t, err)

	assert.Equal(t, "task", info.Name)
	assert.Contains(t, runCtx.Instruction, "You can use the 'task' tool")
	assert.Contains(t, info.Desc, "When using the task tool")
	assert.Contains(t, typedMW.reminder, "Available agent types for the task tool:")
}

func stringPtr(value string) *string {
	return &value
}
