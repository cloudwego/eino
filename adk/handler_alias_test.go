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

package adk_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// aliasMiddleware is written the way a downstream user on the default
// *schema.Message path writes one: the BeforeAgent signature names the
// non-generic adk.ChatModelAgentContext, with no type argument.
//
// This is a compile-time regression guard. If ChatModelAgentContext ever stops
// being an alias for TypedChatModelAgentContext[*schema.Message] — for example by
// becoming a generic type in its own right — this file fails to build and the
// assignment to adk.ChatModelAgentMiddleware below fails with "missing method
// BeforeAgent", which is exactly what downstream code would see.
type aliasMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	gotInstruction string
	gotTools       int
	called         bool
}

func (m *aliasMiddleware) BeforeAgent(ctx context.Context,
	runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {

	m.called = true
	m.gotInstruction = runCtx.Instruction
	m.gotTools = len(runCtx.Tools)

	nCtx := *runCtx
	nCtx.Instruction = runCtx.Instruction + " [alias]"
	return ctx, &nCtx, nil
}

// aliasMiddleware must satisfy the non-generic middleware interface.
var _ adk.ChatModelAgentMiddleware = (*aliasMiddleware)(nil)

// TestChatModelAgentContextAliasCompatibility pins the compatibility alias so a
// hand-written middleware using the non-generic *adk.ChatModelAgentContext keeps
// compiling and keeps being invoked by the runtime.
func TestChatModelAgentContextAliasCompatibility(t *testing.T) {
	t.Run("alias identity", func(t *testing.T) {
		// Both spellings must name the same type, so values are freely assignable
		// in either direction without conversion.
		var viaAlias *adk.ChatModelAgentContext = &adk.TypedChatModelAgentContext[*schema.Message]{
			Instruction: "from typed",
		}
		var viaTyped *adk.TypedChatModelAgentContext[*schema.Message] = viaAlias
		assert.Equal(t, "from typed", viaTyped.Instruction)
	})

	t.Run("middleware is invoked through the alias signature", func(t *testing.T) {
		ctx := context.Background()
		mw := &aliasMiddleware{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}}

		agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:        "alias-agent",
			Description: "verifies the ChatModelAgentContext alias",
			Instruction: "base instruction",
			Model:       &stubChatModel{reply: "done"},
			Handlers:    []adk.ChatModelAgentMiddleware{mw},
		})
		require.NoError(t, err)

		runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})

		iter := runner.Query(ctx, "hi")
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
		}

		assert.True(t, mw.called, "BeforeAgent declared with the alias signature was never called")
		assert.Equal(t, "base instruction", mw.gotInstruction)
		assert.Equal(t, 0, mw.gotTools)
	})
}
