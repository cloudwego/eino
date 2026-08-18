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
	"github.com/cloudwego/eino/schema"
)

func newReminderHandler(b Backend, fn func(context.Context, *FormatReminderInput) (*FormatReminderOutput, error)) *typedSkillHandler[*schema.Message] {
	return &typedSkillHandler[*schema.Message]{
		customFormatReminder: fn,
		tool:                 &typedSkillTool[*schema.Message]{b: b},
	}
}

func TestSkill_CustomFormatReminder(t *testing.T) {
	backend := &inMemoryBackend{m: []Skill{
		{FrontMatter: FrontMatter{Name: "alpha", Description: "d-alpha"}},
	}}

	t.Run("custom rewrites and receives current + changed", func(t *testing.T) {
		var in *FormatReminderInput
		h := newReminderHandler(backend, func(_ context.Context, i *FormatReminderInput) (*FormatReminderOutput, error) {
			in = i
			return &FormatReminderOutput{Reminder: "CUSTOM SKILLS"}, nil
		})
		withRunLocalCtx(t, func(ctx context.Context) {
			state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
			_, state, err := h.BeforeModelRewriteState(ctx, state, nil)
			require.NoError(t, err)

			require.Equal(t, 1, countReminders(state.Messages))
			assert.Equal(t, "CUSTOM SKILLS", firstReminder(state.Messages).Content)

			require.NotNil(t, in)
			assert.Len(t, in.Current, 1)
			assert.Len(t, in.Changed, 1, "fresh run → every skill is changed")
		})
	})

	t.Run("current vs changed distinguishes new from existing skills", func(t *testing.T) {
		twoSkills := &inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "alpha", Description: "d-alpha"}},
			{FrontMatter: FrontMatter{Name: "beta", Description: "d-beta"}},
		}}
		var in *FormatReminderInput
		h := newReminderHandler(twoSkills, func(_ context.Context, i *FormatReminderInput) (*FormatReminderOutput, error) {
			in = i
			return &FormatReminderOutput{Reminder: "kept"}, nil
		})
		withRunLocalCtx(t, func(ctx context.Context) {
			// A prior reminder already advertised alpha (its digest is in history).
			prior := schema.SystemMessage("prior")
			prior.Extra = map[string]any{
				skillsReminderExtraKey: true,
				skillsDigestExtraKey:   []string{skillDigest(FrontMatter{Name: "alpha", Description: "d-alpha"})},
			}
			state := &adk.ChatModelAgentState{Messages: []*schema.Message{prior, schema.UserMessage("hi")}}
			_, _, err := h.BeforeModelRewriteState(ctx, state, nil)
			require.NoError(t, err)

			require.NotNil(t, in)
			assert.Len(t, in.Current, 2, "Current is the full list")
			require.Len(t, in.Changed, 1, "Changed is only the newly-added skill")
			assert.Equal(t, "beta", in.Changed[0].Name)
		})
	})

	t.Run("empty result skips insertion this turn", func(t *testing.T) {
		h := newReminderHandler(backend, func(_ context.Context, _ *FormatReminderInput) (*FormatReminderOutput, error) {
			return &FormatReminderOutput{Reminder: ""}, nil
		})
		withRunLocalCtx(t, func(ctx context.Context) {
			state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
			_, state, err := h.BeforeModelRewriteState(ctx, state, nil)
			require.NoError(t, err)
			assert.Equal(t, 0, countReminders(state.Messages), "empty result → no insert")
		})
	})

	t.Run("error propagates instead of falling back", func(t *testing.T) {
		h := newReminderHandler(backend, func(_ context.Context, _ *FormatReminderInput) (*FormatReminderOutput, error) {
			return nil, errors.New("boom")
		})
		withRunLocalCtx(t, func(ctx context.Context) {
			state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.UserMessage("hi")}}
			_, state, err := h.BeforeModelRewriteState(ctx, state, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "boom")
			assert.Equal(t, 0, countReminders(state.Messages), "no reminder inserted on error")
		})
	})
}
