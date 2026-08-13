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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/internal/systemreminder"
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

func countReminders(msgs []*schema.Message) int {
	n := 0
	for _, m := range msgs {
		if _, ok := m.Extra[skillsReminderExtraKey]; ok {
			n++
		}
	}
	return n
}

func firstReminder(msgs []*schema.Message) *schema.Message {
	for _, m := range msgs {
		if _, ok := m.Extra[skillsReminderExtraKey]; ok {
			return m
		}
	}
	return nil
}

// assertReminderAfterUser checks the skill reminder is a System message positioned
// after the latest user message (never at the front / index 0).
func assertReminderAfterUser(t *testing.T, msgs []*schema.Message) {
	t.Helper()
	lastUser, reminderIdx := -1, -1
	for i, m := range msgs {
		if m.Role == schema.User {
			lastUser = i
		}
		if _, ok := m.Extra[skillsReminderExtraKey]; ok {
			assert.Equal(t, schema.System, m.Role, "reminder must be a system message")
			reminderIdx = i
		}
	}
	require.GreaterOrEqual(t, reminderIdx, 0, "a reminder must be present")
	assert.Greater(t, reminderIdx, lastUser, "reminder must be inserted after the latest user message")
}

// runLocalProbe runs fn from inside BeforeModelRewriteState, i.e. within a live
// agent execution where the run-local state (adk.State.Extra) exists — the same store
// GetRunLocalValue reads. This lets tests seed the pending reminder and drive the
// handler's insertion logic without a full Runner.
type runLocalProbe struct {
	*adk.BaseChatModelAgentMiddleware
	fn func(ctx context.Context)
}

func (p *runLocalProbe) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	p.fn(ctx)
	return ctx, state, nil
}

// withRunLocalCtx executes fn inside a run-local-capable context.
func withRunLocalCtx(t *testing.T, fn func(ctx context.Context)) {
	t.Helper()
	ctx := context.Background()
	a, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "probe-agent",
		Model:         &reproRecordingModel{},
		Handlers:      []adk.ChatModelAgentMiddleware{&runLocalProbe{fn: fn}},
		MaxIterations: 1,
	})
	require.NoError(t, err)
	iter := a.Run(ctx, &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}})
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}
}

// markHandled sets the run-local "already refreshed this Run" mark, mirroring what the
// first BeforeModelRewriteState call does after it inserts. A later model call in the same
// Run reads it back and skips, so the reminder is refreshed at most once per turn.
func markHandled(t *testing.T, ctx context.Context) {
	t.Helper()
	require.NoError(t, adk.SetRunLocalValue(ctx, skillsHandledKey, true))
}

// TestSkill_BeforeModelRewriteState_InsertsOnFreshRun verifies the first-call insertion:
// with no handled mark yet (a fresh Run), BeforeModelRewriteState lists skills and inserts
// exactly one System reminder after the user message, listing the section and carrying the
// digest array in Extra.
func TestSkill_BeforeModelRewriteState_InsertsOnFreshRun(t *testing.T) {
	h := &typedSkillHandler[*schema.Message]{
		tool: &typedSkillTool[*schema.Message]{b: &inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
		}}},
	}
	skills := []FrontMatter{{Name: "alpha", Description: "desc-alpha"}}
	digests := []string{skillDigest(skills[0])}

	withRunLocalCtx(t, func(ctx context.Context) {
		// No handled mark → this is the turn's first model call → insert.
		state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, state, err := h.BeforeModelRewriteState(ctx, state, nil)
		require.NoError(t, err)

		require.Equal(t, 1, countReminders(state.Messages), "one reminder inserted")
		assertReminderAfterUser(t, state.Messages)

		rem := firstReminder(state.Messages)
		require.NotNil(t, rem)
		assert.Contains(t, rem.Content, "alpha")
		assert.Contains(t, rem.Content, "desc-alpha")
		// The digest array is stored in Extra for the next turn's diff.
		assert.Equal(t, digests, toStringSlice(rem.Extra[skillsDigestExtraKey]))
		// And it is readable back via systemreminder.LatestExtra, the exact path the next turn's
		// diff uses.
		got, ok := systemreminder.LatestExtra(state.Messages, skillsReminderExtraKey, skillsDigestExtraKey)
		require.True(t, ok)
		assert.Equal(t, digests, toStringSlice(got))
	})
}

// TestSkill_BeforeModelRewriteState_NoInsertWhenHandled verifies that once the run-local
// handled mark is set (an earlier model call this Run already refreshed the reminder),
// BeforeModelRewriteState inserts nothing.
func TestSkill_BeforeModelRewriteState_NoInsertWhenHandled(t *testing.T) {
	h := &typedSkillHandler[*schema.Message]{
		tool: &typedSkillTool[*schema.Message]{b: &inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
		}}},
	}

	withRunLocalCtx(t, func(ctx context.Context) {
		// Simulate an earlier model call in this Run having already refreshed the reminder.
		markHandled(t, ctx)
		state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, state, err := h.BeforeModelRewriteState(ctx, state, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, countReminders(state.Messages), "handled mark set → no insert")
	})
}

// TestSkill_InsertOncePerTurn verifies BeforeModelRewriteState inserts the reminder
// exactly once: the first call lists+inserts and sets the handled mark; a second call in
// the same Run finds the mark set and inserts nothing.
func TestSkill_InsertOncePerTurn(t *testing.T) {
	h := &typedSkillHandler[*schema.Message]{
		tool: &typedSkillTool[*schema.Message]{b: &inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "alpha", Description: "first"}},
		}}},
	}

	withRunLocalCtx(t, func(ctx context.Context) {
		s1 := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, s1, err := h.BeforeModelRewriteState(ctx, s1, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, countReminders(s1.Messages), "first call inserts one reminder")

		s2 := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, s2, err = h.BeforeModelRewriteState(ctx, s2, nil)
		require.NoError(t, err)
		assert.Equal(t, 0, countReminders(s2.Messages), "handled mark set → second call inserts nothing")
	})
}

// TestSkill_BeforeAgentToRewriteState_Bridge exercises the real end-to-end path a Runner
// takes: BeforeAgent injects the tool + instruction (no run-local, no List), then the
// first BeforeModelRewriteState of the turn lists+diffs and inserts. The handled mark is
// self-managed inside BeforeModelRewriteState — BeforeAgent no longer seeds it.
func TestSkill_BeforeAgentToRewriteState_Bridge(t *testing.T) {
	newHandler := func(b Backend) *typedSkillHandler[*schema.Message] {
		mw, err := NewMiddleware(context.Background(), &Config{Backend: b})
		require.NoError(t, err)
		return mw.(*typedSkillHandler[*schema.Message])
	}

	t.Run("added skill bridges through to an inserted reminder", func(t *testing.T) {
		h := newHandler(&inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "alpha", Description: "d-alpha"}},
		}})
		withRunLocalCtx(t, func(ctx context.Context) {
			rc := &adk.ChatModelAgentContext[*schema.Message]{
				AgentInput: &adk.TypedAgentInput[*schema.Message]{Messages: []*schema.Message{schema.UserMessage("hi")}},
			}
			_, _, err := h.BeforeAgent(ctx, rc)
			require.NoError(t, err)

			state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
			_, state, err = h.BeforeModelRewriteState(ctx, state, nil)
			require.NoError(t, err)
			require.Equal(t, 1, countReminders(state.Messages), "first model call of the turn must insert")
			assert.Contains(t, firstReminder(state.Messages).Content, "alpha")
		})
	})

	t.Run("unchanged skills bridge to no insertion", func(t *testing.T) {
		h := newHandler(&inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "alpha", Description: "d-alpha"}},
		}})
		withRunLocalCtx(t, func(ctx context.Context) {
			prior := schema.SystemMessage("AVAILABLE SKILLS\n- alpha: d-alpha")
			prior.Extra = map[string]any{
				skillsReminderExtraKey: true,
				skillsDigestExtraKey:   []string{skillDigest(FrontMatter{Name: "alpha", Description: "d-alpha"})},
			}
			rc := &adk.ChatModelAgentContext[*schema.Message]{
				AgentInput: &adk.TypedAgentInput[*schema.Message]{Messages: []*schema.Message{schema.UserMessage("hi")}},
			}
			_, _, err := h.BeforeAgent(ctx, rc)
			require.NoError(t, err)

			// The prior reminder lives in the full history (state.Messages), which is
			// where BeforeModelRewriteState diffs — not in AgentInput.Messages.
			state := &adk.ChatModelAgentState{Messages: []*schema.Message{prior, schema.UserMessage("hi")}}
			_, state, err = h.BeforeModelRewriteState(ctx, state, nil)
			require.NoError(t, err)
			assert.Equal(t, 1, countReminders(state.Messages), "no digest change → no new insertion")
		})
	})
}

// TestSkill_BeforeAgent_BackendStates verifies BeforeAgent is non-destructive and
// error-free across backend states — with skills, with zero skills, on List error, and
// when a prior skill reminder (carrying a digest array) is already in history. BeforeAgent
// no longer calls List(); the actual listing + insertion is driven by
// BeforeModelRewriteState (see the tests above).
func TestSkill_BeforeAgent_BackendStates(t *testing.T) {
	ctx := context.Background()

	newHandler := func(b Backend) *typedSkillHandler[*schema.Message] {
		mw, err := NewMiddleware(ctx, &Config{Backend: b})
		require.NoError(t, err)
		return mw.(*typedSkillHandler[*schema.Message])
	}

	user := schema.UserMessage("hi")

	cases := map[string]Backend{
		"with skills": &inMemoryBackend{m: []Skill{{FrontMatter: FrontMatter{Name: "alpha", Description: "d"}}}},
		"zero skills": &inMemoryBackend{m: []Skill{}},
		"list error":  &errorBackend{listErr: errors.New("list failed")},
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHandler(b)
			rc := &adk.ChatModelAgentContext[*schema.Message]{
				AgentInput: &adk.TypedAgentInput[*schema.Message]{Messages: []*schema.Message{user}},
			}
			_, nrc, err := h.BeforeAgent(ctx, rc)
			require.NoError(t, err)
			require.Len(t, nrc.AgentInput.Messages, 1, "BeforeAgent must not mutate messages")
			assert.Equal(t, user, nrc.AgentInput.Messages[0])
		})
	}

	t.Run("prior reminder in history does not affect BeforeAgent (diff moved to rewrite-state)", func(t *testing.T) {
		h := newHandler(&inMemoryBackend{m: []Skill{{FrontMatter: FrontMatter{Name: "alpha", Description: "d"}}}})
		prior := schema.SystemMessage("AVAILABLE SKILLS\n- alpha: d")
		prior.Extra = map[string]any{
			skillsReminderExtraKey: true,
			skillsDigestExtraKey:   []string{skillDigest(FrontMatter{Name: "alpha", Description: "d"})},
		}
		rc := &adk.ChatModelAgentContext[*schema.Message]{
			AgentInput: &adk.TypedAgentInput[*schema.Message]{Messages: []*schema.Message{prior, user}},
		}
		_, nrc, err := h.BeforeAgent(ctx, rc)
		require.NoError(t, err)
		require.Len(t, nrc.AgentInput.Messages, 2, "BeforeAgent must not mutate messages")
	})
}
