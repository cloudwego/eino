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

package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetMessageID(t *testing.T) {
	t.Run("nil extra returns empty", func(t *testing.T) {
		assert.Equal(t, "", GetMessageID(nil))
	})

	t.Run("empty extra returns empty", func(t *testing.T) {
		assert.Equal(t, "", GetMessageID(map[string]any{}))
	})

	t.Run("wrong type returns empty", func(t *testing.T) {
		extra := map[string]any{EinoMsgIDKey: 123}
		assert.Equal(t, "", GetMessageID(extra))
	})

	t.Run("returns set ID", func(t *testing.T) {
		extra := map[string]any{EinoMsgIDKey: "test-id-123"}
		assert.Equal(t, "test-id-123", GetMessageID(extra))
	})
}

func TestSetMessageID(t *testing.T) {
	t.Run("nil extra creates map", func(t *testing.T) {
		extra := SetMessageID(nil, "id-1")
		assert.NotNil(t, extra)
		assert.Equal(t, "id-1", extra[EinoMsgIDKey])
	})

	t.Run("existing extra preserved", func(t *testing.T) {
		extra := map[string]any{"other_key": "other_val"}
		result := SetMessageID(extra, "id-2")
		assert.Equal(t, "id-2", result[EinoMsgIDKey])
		assert.Equal(t, "other_val", result["other_key"])
	})
}

func TestEnsureMessageID(t *testing.T) {
	t.Run("nil extra gets ID", func(t *testing.T) {
		extra := EnsureMessageID(nil)
		id := GetMessageID(extra)
		assert.NotEmpty(t, id)
		assert.Len(t, id, 36) // UUID v4 format: 8-4-4-4-12 = 36 chars
	})

	t.Run("idempotent - does not overwrite existing ID", func(t *testing.T) {
		extra := SetMessageID(nil, "existing-id")
		result := EnsureMessageID(extra)
		assert.Equal(t, "existing-id", GetMessageID(result))
	})

	t.Run("empty extra gets new ID", func(t *testing.T) {
		extra := map[string]any{}
		result := EnsureMessageID(extra)
		id := GetMessageID(result)
		assert.NotEmpty(t, id)
		assert.Len(t, id, 36)
	})

	t.Run("generates unique IDs", func(t *testing.T) {
		extra1 := EnsureMessageID(nil)
		extra2 := EnsureMessageID(nil)
		assert.NotEqual(t, GetMessageID(extra1), GetMessageID(extra2))
	})
}
