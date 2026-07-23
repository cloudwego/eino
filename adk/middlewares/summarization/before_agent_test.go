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

package summarization

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// TestBeforeAgent_AppendsContextManagementNote covers BeforeAgent and the
// getContextManagementInstruction helper it relies on.
func TestBeforeAgent_AppendsContextManagementNote(t *testing.T) {
	ctx := context.Background()
	mw := &TypedMiddleware[*schema.Message]{}
	note := getContextManagementInstruction()
	require.NotEmpty(t, note, "context-management note should be non-empty")

	// nil runCtx: returned unchanged.
	_, rc, err := mw.BeforeAgent(ctx, nil)
	require.NoError(t, err)
	assert.Nil(t, rc)

	// Empty instruction: the note becomes the whole instruction.
	_, rc, err = mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	require.NotNil(t, rc)
	assert.Equal(t, note, strings.TrimSpace(rc.Instruction))

	// Non-empty instruction: the note is appended after the base instruction.
	_, rc2, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{Instruction: "base instruction"})
	require.NoError(t, err)
	require.NotNil(t, rc2)
	assert.Contains(t, rc2.Instruction, "base instruction")
	assert.Contains(t, rc2.Instruction, note)

	// Idempotent: an instruction that already contains the note is returned unchanged.
	_, rc3, err := mw.BeforeAgent(ctx, rc2)
	require.NoError(t, err)
	assert.Equal(t, rc2.Instruction, rc3.Instruction)
	assert.Equal(t, strings.Count(rc2.Instruction, note), strings.Count(rc3.Instruction, note))
}
