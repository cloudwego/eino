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
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// recordingModel records the input it was given on each call and always returns
// a final answer (no tool calls), so every turn is a single model call that ends
// the turn.
type recordingModel struct {
	inputs [][]*schema.Message
}

func (m *recordingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	cp := make([]*schema.Message, len(input))
	copy(cp, input)
	m.inputs = append(m.inputs, cp)
	return schema.AssistantMessage("done", nil), nil
}

func (m *recordingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func drainEvents(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	t.Helper()
	for {
		event, ok := iter.Next()
		if !ok {
			return
		}
		assert.NoError(t, event.Err)
	}
}

func countReminders(msgs []*schema.Message, extraKey string) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role != schema.System {
			continue
		}
		if _, ok := msg.Extra[extraKey]; ok {
			n++
		}
	}
	return n
}

// TestSubagent_ReminderPersistedInsertedOnce verifies the reminder is inserted
// exactly once via BeforeModelRewriteState: it IS persisted as a single
// MessageInserted session event, is present once in each turn's model input, and
// is never re-inserted or duplicated across turns (Has skips re-insertion once the
// reminder is in the reconstructed history).
func TestSubagent_ReminderPersistedInsertedOnce(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)
	const sessionID = "subagent-reminder-session"

	mw, err := New(ctx, &Config{
		SubAgents: []adk.Agent{&mockAgent{name: "worker", desc: "does work"}},
	})
	require.NoError(t, err)

	newAgent := func(m model.BaseChatModel) adk.Agent {
		a, aerr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        "reminder-agent",
			Description: "reminder persistence test agent",
			Model:       m,
			Handlers:    []adk.ChatModelAgentMiddleware{mw},
		})
		require.NoError(t, aerr)
		return a
	}

	countPersistedReminders := func() int {
		res, lerr := store.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{})
		require.NoError(t, lerr)
		n := 0
		for _, ev := range res.Events {
			if ev.MessageInserted == nil {
				continue
			}
			if _, ok := ev.MessageInserted.Message.Extra[agentTypesReminderExtraKey]; ok {
				n++
			}
		}
		return n
	}

	// Turn 1.
	model1 := &recordingModel{}
	runner1 := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        newAgent(model1),
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainEvents(t, runner1.Query(ctx, "hello"))
	require.NotEmpty(t, model1.inputs)
	// The reminder was injected for the model call this turn.
	assert.Equal(t, 1, countReminders(model1.inputs[0], agentTypesReminderExtraKey),
		"turn 1 model input should carry exactly one reminder")

	// The reminder IS persisted as exactly one MessageInserted session event.
	assert.Equal(t, 1, countPersistedReminders(), "reminder must be persisted exactly once")

	// Turn 2 on the same session.
	model2 := &recordingModel{}
	runner2 := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:        newAgent(model2),
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainEvents(t, runner2.Query(ctx, "again"))

	require.NotEmpty(t, model2.inputs)
	turn2Input := model2.inputs[0]

	// Exactly one reminder is present: replayed from the persisted event, not
	// re-inserted this turn — so no duplication.
	assert.Equal(t, 1, countReminders(turn2Input, agentTypesReminderExtraKey),
		"turn 2 must see exactly one reminder with no duplication")

	// Still persisted exactly once: turn 2 did not insert a second reminder.
	assert.Equal(t, 1, countPersistedReminders(), "reminder still persisted exactly once after turn 2")
}
