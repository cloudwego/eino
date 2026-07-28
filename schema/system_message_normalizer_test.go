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

package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertNonLeadingSystemMessagesToUser(t *testing.T) {
	sys1 := SystemMessage("prompt A")
	sys2 := SystemMessage("prompt B")
	user := UserMessage("hi")
	midSys := SystemMessage("reminder")
	asst := AssistantMessage("ok", nil)

	orig := []*Message{sys1, sys2, user, midSys, asst}
	got := ConvertNonLeadingSystemMessagesToUser(orig)

	// Leading consecutive system block (the system prompt) is preserved.
	assert.Equal(t, System, got[0].Role)
	assert.Equal(t, System, got[1].Role)
	assert.Equal(t, User, got[2].Role)
	// The non-leading system message is rewritten to user, content kept.
	assert.Equal(t, User, got[3].Role)
	assert.Equal(t, "reminder", got[3].Content)
	assert.Equal(t, Assistant, got[4].Role)

	// Copy-on-write: originals untouched.
	assert.Same(t, midSys, orig[3])
	assert.Equal(t, System, orig[3].Role)

	// No non-leading system message => same slice returned, no allocation.
	noMid := []*Message{sys1, user}
	assert.Equal(t, System, ConvertNonLeadingSystemMessagesToUser(noMid)[0].Role)
}

func TestConvertNonLeadingSystemMessagesToUser_Idempotent(t *testing.T) {
	// Two conversion passes must equal one: a second pass finds no non-leading
	// system messages to convert.
	in := []*Message{
		SystemMessage("prompt"),
		UserMessage("hi"),
		SystemMessage("reminder"),
		AssistantMessage("ok", nil),
	}
	once := ConvertNonLeadingSystemMessagesToUser(in)
	twice := ConvertNonLeadingSystemMessagesToUser(once)

	require.Len(t, twice, 4)
	assert.Equal(t, System, twice[0].Role) // leading prompt preserved
	assert.Equal(t, User, twice[1].Role)
	assert.Equal(t, User, twice[2].Role) // reminder stays user, not re-touched
	assert.Equal(t, "reminder", twice[2].Content)
	assert.Equal(t, Assistant, twice[3].Role)

	// Second pass changed nothing => same slice returned (no allocation).
	require.Len(t, once, len(twice))
	for i := range once {
		assert.Same(t, once[i], twice[i])
	}
}

func TestConvertNonLeadingSystemAgenticMessagesToUser(t *testing.T) {
	sys := SystemAgenticMessage("prompt")
	user := UserAgenticMessage("hi")
	midSys := SystemAgenticMessage("reminder")

	orig := []*AgenticMessage{sys, user, midSys}
	got := ConvertNonLeadingSystemAgenticMessagesToUser(orig)

	// Leading system prompt preserved; non-leading system rewritten to user.
	assert.Equal(t, AgenticRoleTypeSystem, got[0].Role)
	assert.Equal(t, AgenticRoleTypeUser, got[1].Role)
	assert.Equal(t, AgenticRoleTypeUser, got[2].Role)

	// Copy-on-write: originals untouched.
	assert.Same(t, midSys, orig[2])
	assert.Equal(t, AgenticRoleTypeSystem, orig[2].Role)

	// Idempotent: a second pass returns the same slice unchanged.
	twice := ConvertNonLeadingSystemAgenticMessagesToUser(got)
	require.Len(t, twice, len(got))
	for i := range got {
		assert.Same(t, got[i], twice[i])
	}
}
