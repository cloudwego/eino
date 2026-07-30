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

// TestSkill_BeforeAgent_AgenticMessage exercises the *schema.AgenticMessage path.
// BeforeAgent no longer mutates the message list (it stashes the pending reminder as
// run-local state; BeforeModelRewriteState does the insertion). This test verifies
// BeforeAgent still injects the tool and never touches AgentInput.Messages.
func TestSkill_BeforeAgent_AgenticMessage(t *testing.T) {
	ctx := context.Background()
	backend := &inMemoryBackend{m: []Skill{{FrontMatter: FrontMatter{Name: "alpha", Description: "first"}}}}
	mw, err := NewTyped[*schema.AgenticMessage](ctx, &TypedConfig[*schema.AgenticMessage]{Backend: backend})
	require.NoError(t, err)
	h := mw.(*typedSkillHandler[*schema.AgenticMessage])

	user := schema.UserAgenticMessage("hi")
	runCtx := &adk.ChatModelAgentContext[*schema.AgenticMessage]{
		AgentInput: &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user}},
	}

	// Messages are left untouched by BeforeAgent; the tool is injected.
	_, nrc, err := h.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Len(t, nrc.AgentInput.Messages, 1)
	assert.Equal(t, user, nrc.AgentInput.Messages[0])
	assert.Len(t, nrc.Tools, 1)

	// No skills: BeforeAgent still does not touch messages.
	empty, err := NewTyped[*schema.AgenticMessage](ctx, &TypedConfig[*schema.AgenticMessage]{Backend: &inMemoryBackend{}})
	require.NoError(t, err)
	hEmpty := empty.(*typedSkillHandler[*schema.AgenticMessage])
	rcEmpty := &adk.ChatModelAgentContext[*schema.AgenticMessage]{
		AgentInput: &adk.TypedAgentInput[*schema.AgenticMessage]{Messages: []*schema.AgenticMessage{user}},
	}
	_, nrcEmpty, err := hEmpty.BeforeAgent(ctx, rcEmpty)
	require.NoError(t, err)
	assert.Len(t, nrcEmpty.AgentInput.Messages, 1)

	// nil AgentInput: guarded, no panic; instruction/tools still applied.
	rcNil := &adk.ChatModelAgentContext[*schema.AgenticMessage]{}
	_, nrcNil, err := h.BeforeAgent(ctx, rcNil)
	require.NoError(t, err)
	assert.Len(t, nrcNil.Tools, 1)
}
