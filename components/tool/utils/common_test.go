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

package utils

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMarshalString(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
		hasError bool
	}{
		{
			name:     "string input should return as-is",
			input:    "hello world",
			expected: "hello world",
			hasError: false,
		},
		{
			name:     "empty string should return empty",
			input:    "",
			expected: "",
			hasError: false,
		},
		{
			name:     "string with special characters",
			input:    "hello\nworld\t\"test\"",
			expected: "hello\nworld\t\"test\"",
			hasError: false,
		},
		{
			name:     "string with unicode",
			input:    "你好世界",
			expected: "你好世界",
			hasError: false,
		},
		{
			name:     "integer should be marshaled to JSON",
			input:    42,
			expected: "42",
			hasError: false,
		},
		{
			name:     "float should be marshaled to JSON",
			input:    3.14,
			expected: "3.14",
			hasError: false,
		},
		{
			name:     "boolean true should be marshaled to JSON",
			input:    true,
			expected: "true",
			hasError: false,
		},
		{
			name:     "boolean false should be marshaled to JSON",
			input:    false,
			expected: "false",
			hasError: false,
		},
		{
			name:     "nil should be marshaled to JSON null",
			input:    nil,
			expected: "null",
			hasError: false,
		},
		{
			name:     "slice should be marshaled to JSON array",
			input:    []int{1, 2, 3},
			expected: "[1,2,3]",
			hasError: false,
		},
		{
			name:     "empty slice should be marshaled to JSON empty array",
			input:    []int{},
			expected: "[]",
			hasError: false,
		},
		{
			name:     "empty map should be marshaled to JSON empty object",
			input:    map[string]int{},
			expected: "{}",
			hasError: false,
		},
		{
			name:     "struct should be marshaled to JSON",
			input:    struct{ Name string }{Name: "test"},
			expected: `{"Name":"test"}`,
			hasError: false,
		},
		{
			name:     "pointer to string should be handled as non-string",
			input:    func() *string { s := "test"; return &s }(),
			expected: `"test"`,
			hasError: false,
		},
		{
			name:     "interface{} containing string should return as-is",
			input:    interface{}("test string"),
			expected: "test string",
			hasError: false,
		},
		{
			name:     "interface{} containing int should be marshaled",
			input:    interface{}(123),
			expected: "123",
			hasError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := marshalString(tt.input)

			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestMarshalStringEdgeCases(t *testing.T) {
	t.Run("complex nested structure", func(t *testing.T) {
		complex := map[string]interface{}{
			"string": "value",
			"number": 42,
			"nested": map[string]interface{}{
				"array": []string{"a", "b", "c"},
				"bool":  true,
			},
		}

		result, err := marshalString(complex)
		assert.NoError(t, err)
		assert.Contains(t, result, `"string":"value"`)
		assert.Contains(t, result, `"number":42`)
		assert.Contains(t, result, `"nested"`)
	})

	t.Run("string type assertion priority", func(t *testing.T) {
		// Test that string type assertion has priority over JSON marshaling
		var input interface{} = "direct string"
		result, err := marshalString(input)
		assert.NoError(t, err)
		assert.Equal(t, "direct string", result)

		// Verify it's not JSON encoded
		assert.NotEqual(t, `"direct string"`, result)
	})
}

func TestMarshalStringConsistency(t *testing.T) {
	t.Run("string vs JSON marshaling difference", func(t *testing.T) {
		input := `{"key": "value"}`

		// Direct string should return as-is
		result, err := marshalString(input)
		assert.NoError(t, err)
		assert.Equal(t, input, result)

		// Should not be double-encoded
		assert.NotEqual(t, fmt.Sprintf(`"%s"`, input), result)
	})
}
