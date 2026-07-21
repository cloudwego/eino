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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type reproRecordingModel struct {
	inputs [][]*schema.Message
}

func (m *reproRecordingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	cp := make([]*schema.Message, len(input))
	copy(cp, input)
	m.inputs = append(m.inputs, cp)
	return schema.AssistantMessage("done", nil), nil
}

func (m *reproRecordingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func drainSkillEvents(t *testing.T, iter *adk.AsyncIterator[*adk.AgentEvent]) {
	t.Helper()
	for {
		ev, ok := iter.Next()
		if !ok {
			return
		}
		assert.NoError(t, ev.Err)
	}
}

func TestSkill_ReminderUpdatesWhenSkillAddedBetweenTurns(t *testing.T) {
	ctx := context.Background()
	backend := &inMemoryBackend{m: []Skill{
		{FrontMatter: FrontMatter{Name: "alpha", Description: "first skill"}},
	}}
	store := session.NewInMemoryStore[*schema.Message](nil)
	const sessionID = "skill-refresh-session"

	mw, err := NewMiddleware(ctx, &Config{Backend: backend})
	require.NoError(t, err)

	newAgent := func(m model.BaseChatModel) adk.Agent {
		a, aerr := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:     "skill-agent",
			Model:    m,
			Handlers: []adk.ChatModelAgentMiddleware{mw},
		})
		require.NoError(t, aerr)
		return a
	}

	// Turn 1: only "alpha" exists.
	model1 := &reproRecordingModel{}
	runner1 := adk.NewRunner(ctx, adk.RunnerConfig{Agent: newAgent(model1), SessionID: sessionID, SessionStore: store})
	drainSkillEvents(t, runner1.Query(ctx, "hi"))
	require.NotEmpty(t, model1.inputs)
	turn1 := model1.inputs[len(model1.inputs)-1]
	assert.True(t, sectionMentions(turn1, "alpha"), "turn 1 reminder should list alpha")
	assert.False(t, sectionMentions(turn1, "beta"), "turn 1 must not mention beta yet")

	// Between turns: install a new skill.
	backend.m = append(backend.m, Skill{FrontMatter: FrontMatter{Name: "beta", Description: "second skill"}})

	// Turn 2: "beta" should now appear in a reminder.
	model2 := &reproRecordingModel{}
	runner2 := adk.NewRunner(ctx, adk.RunnerConfig{Agent: newAgent(model2), SessionID: sessionID, SessionStore: store})
	drainSkillEvents(t, runner2.Query(ctx, "again"))
	require.NotEmpty(t, model2.inputs)
	turn2 := model2.inputs[len(model2.inputs)-1]

	// Confirm turn 2 actually replayed the session history (not a fresh start),
	// so the refresh is exercised against a reconstructed message list.
	assert.True(t, containsUserText(turn2, "hi"), "turn 2 must include turn 1 history via session reconstruction")

	assert.True(t, sectionMentions(turn2, "beta"),
		"turn 2 model input must contain a reminder that lists the newly added skill beta")
}

func containsUserText(msgs []*schema.Message, text string) bool {
	for _, m := range msgs {
		if m.Role == schema.User && strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

func sectionMentions(msgs []*schema.Message, name string) bool {
	for _, m := range msgs {
		if m.Role != schema.System {
			continue
		}
		if _, ok := m.Extra[skillsReminderExtraKey]; ok && strings.Contains(m.Content, name) {
			return true
		}
	}
	return false
}
