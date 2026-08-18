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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func reminderOfMiddleware(t *testing.T, mw adk.TypedChatModelAgentMiddleware[*schema.Message]) string {
	t.Helper()
	return mw.(*typedSubagentMiddleware[*schema.Message]).reminder
}

func countAgentTypesReminders(msgs []*schema.Message) int {
	n := 0
	for _, m := range msgs {
		if _, ok := m.Extra[agentTypesReminderExtraKey]; ok {
			n++
		}
	}
	return n
}

func TestSubagent_CustomFormatReminder(t *testing.T) {
	ctx := context.Background()
	subAgents := []adk.TypedAgent[*schema.Message]{
		&mockAgent{name: "researcher", desc: "does research"},
		&mockAgent{name: "coder", desc: "writes code"},
	}

	t.Run("nil keeps default reminder", func(t *testing.T) {
		mw, err := NewTyped[*schema.Message](ctx, &TypedConfig[*schema.Message]{SubAgents: subAgents})
		require.NoError(t, err)
		rem := reminderOfMiddleware(t, mw)
		assert.Contains(t, rem, "researcher")
		assert.Contains(t, rem, "does research")
	})

	t.Run("custom rewrites and receives sub-agents", func(t *testing.T) {
		var gotAgents []adk.TypedAgent[*schema.Message]
		mw, err := NewTyped[*schema.Message](ctx, &TypedConfig[*schema.Message]{
			SubAgents: subAgents,
			CustomFormatReminder: func(_ context.Context, in *FormatReminderInput[*schema.Message]) (*FormatReminderOutput, error) {
				gotAgents = in.SubAgents
				return &FormatReminderOutput{Reminder: "CUSTOM AGENTS"}, nil
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "CUSTOM AGENTS", reminderOfMiddleware(t, mw))
		assert.Len(t, gotAgents, 2)
	})

	t.Run("error propagates and fails construction", func(t *testing.T) {
		_, err := NewTyped[*schema.Message](ctx, &TypedConfig[*schema.Message]{
			SubAgents: subAgents,
			CustomFormatReminder: func(_ context.Context, _ *FormatReminderInput[*schema.Message]) (*FormatReminderOutput, error) {
				return nil, errors.New("boom")
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("empty result suppresses the reminder message", func(t *testing.T) {
		mw, err := NewTyped[*schema.Message](ctx, &TypedConfig[*schema.Message]{
			SubAgents: subAgents,
			CustomFormatReminder: func(_ context.Context, _ *FormatReminderInput[*schema.Message]) (*FormatReminderOutput, error) {
				return &FormatReminderOutput{Reminder: ""}, nil
			},
		})
		require.NoError(t, err)
		require.Empty(t, reminderOfMiddleware(t, mw))

		m := mw.(*typedSubagentMiddleware[*schema.Message])
		state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, countAgentTypesReminders(state.Messages), "empty reminder → nothing inserted")
	})
}
