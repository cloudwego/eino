/*
 * Copyright 2025 CloudWeGo Authors
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

package gemini

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConcatResponseMetaExtensions(t *testing.T) {
	t.Run("multiple extensions - takes last non-empty values", func(t *testing.T) {
		meta1 := &GroundingMetadata{WebSearchQueries: []string{"query1"}}
		meta2 := &GroundingMetadata{WebSearchQueries: []string{"query2"}}

		exts := []*ResponseMetaExtension{
			{
				ID:            "resp_1",
				FinishReason:  "STOP",
				GroundingMeta: meta1,
			},
			{
				ID:            "resp_2",
				FinishReason:  "",
				GroundingMeta: nil,
			},
			{
				ID:            "",
				FinishReason:  "MAX_TOKENS",
				GroundingMeta: meta2,
			},
		}

		result, err := ConcatResponseMetaExtensions(exts)
		assert.NoError(t, err)
		assert.Equal(t, "resp_2", result.ID)
		assert.Equal(t, "MAX_TOKENS", result.FinishReason)
		assert.Equal(t, meta2, result.GroundingMeta)
	})

	t.Run("streaming scenario", func(t *testing.T) {
		meta := &GroundingMetadata{
			GroundingChunks: []*GroundingChunk{
				{
					Web: &GroundingChunkWeb{
						Title: "Example",
						URI:   "https://example.com",
					},
				},
			},
		}

		exts := []*ResponseMetaExtension{
			{ID: "stream_123", FinishReason: "", GroundingMeta: nil},
			{ID: "", FinishReason: "", GroundingMeta: nil},
			{ID: "", FinishReason: "STOP", GroundingMeta: meta},
		}

		result, err := ConcatResponseMetaExtensions(exts)
		assert.NoError(t, err)
		assert.Equal(t, "stream_123", result.ID)
		assert.Equal(t, "STOP", result.FinishReason)
		assert.Equal(t, meta, result.GroundingMeta)
	})
}
