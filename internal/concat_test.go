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

package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConcat(t *testing.T) {
	t.Run("concat map chunks with nil value", func(t *testing.T) {
		c1 := map[string]any{
			"a": map[string]any{
				"b": map[string]any{
					"c1": nil,
				},
			},
		}
		c2 := map[string]any{
			"a": map[string]any{
				"b": map[string]any{
					"c2": "c2",
				},
			},
		}
		m, err := ConcatItems([]map[string]any{c1, c2})
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{
			"a": map[string]any{
				"b": map[string]any{
					"c1": nil,
					"c2": "c2",
				},
			},
		}, m)
	})
}
