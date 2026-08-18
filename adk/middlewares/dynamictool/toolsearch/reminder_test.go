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

package toolsearch

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func countToolSearchReminders(msgs []*schema.Message) int {
	n := 0
	for _, m := range msgs {
		if _, ok := m.Extra[toolSearchReminderExtraKey]; ok {
			n++
		}
	}
	return n
}

func TestToolSearch_CustomFormatReminder(t *testing.T) {
	ctx := context.Background()
	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}

	reminderOf := func(mw adk.ChatModelAgentMiddleware) string {
		return mw.(*typedMiddleware[*schema.Message]).reminder
	}

	t.Run("nil keeps default reminder", func(t *testing.T) {
		mw, err := New(ctx, &Config{DynamicTools: []tool.BaseTool{dynamicA, dynamicB}})
		require.NoError(t, err)
		assert.Contains(t, reminderOf(mw), "dynamic_tool_a")
	})

	t.Run("custom rewrites and receives tool infos", func(t *testing.T) {
		var gotTools []*schema.ToolInfo
		mw, err := New(ctx, &Config{
			DynamicTools: []tool.BaseTool{dynamicA, dynamicB},
			CustomFormatReminder: func(_ context.Context, in *FormatReminderInput) (*FormatReminderOutput, error) {
				gotTools = in.DynamicTools
				return &FormatReminderOutput{Reminder: "CUSTOM REMINDER"}, nil
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "CUSTOM REMINDER", reminderOf(mw))
		require.Len(t, gotTools, 2, "formatter gets full ToolInfos, not just names")
		assert.Equal(t, "Dynamic tool A", gotTools[0].Desc, "ToolInfo carries description beyond the name")
	})

	t.Run("error propagates and fails construction", func(t *testing.T) {
		_, err := New(ctx, &Config{
			DynamicTools: []tool.BaseTool{dynamicA},
			CustomFormatReminder: func(_ context.Context, _ *FormatReminderInput) (*FormatReminderOutput, error) {
				return nil, errors.New("boom")
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("empty result suppresses the reminder message", func(t *testing.T) {
		mw, err := New(ctx, &Config{
			DynamicTools: []tool.BaseTool{dynamicA},
			CustomFormatReminder: func(_ context.Context, _ *FormatReminderInput) (*FormatReminderOutput, error) {
				return &FormatReminderOutput{Reminder: ""}, nil
			},
		})
		require.NoError(t, err)
		require.Empty(t, reminderOf(mw))

		m := mw.(*typedMiddleware[*schema.Message])
		state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, countToolSearchReminders(state.Messages), "empty reminder → nothing inserted")
	})

	t.Run("default reminder is inserted once", func(t *testing.T) {
		mw, err := New(ctx, &Config{DynamicTools: []tool.BaseTool{dynamicA}})
		require.NoError(t, err)
		m := mw.(*typedMiddleware[*schema.Message])
		state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, countToolSearchReminders(state.Messages))
	})
}
