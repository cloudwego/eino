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

package schema

import (
	"bytes"
	"reflect"
	"testing"
)

type testStruct struct{}

func TestGetTypeName(t *testing.T) {
	type localNamedType struct{}

	testCases := []struct {
		name     string
		input    reflect.Type
		expected string
	}{
		{
			name:     "named type from current package",
			input:    reflect.TypeOf(testStruct{}),
			expected: "github.com/cloudwego/eino/schema.testStruct",
		},
		{
			name:     "pointer to named type from current package",
			input:    reflect.TypeOf(&testStruct{}),
			expected: "*github.com/cloudwego/eino/schema.testStruct",
		},
		{
			name:     "unnamed map type",
			input:    reflect.TypeOf(map[string]int{}),
			expected: "map[string]int",
		},
		{
			name:     "pointer to unnamed map type",
			input:    reflect.TypeOf(new(map[string]int)),
			expected: "*map[string]int",
		},
		{
			name:     "built-in type",
			input:    reflect.TypeOf(0),
			expected: "int",
		},
		{
			name:     "pointer to built-in type",
			input:    reflect.TypeOf(new(int)),
			expected: "*int",
		},
		{
			name:     "named type from standard library",
			input:    reflect.TypeOf(bytes.Buffer{}),
			expected: "bytes.Buffer",
		},
		{
			name:     "pointer to named type from standard library",
			input:    reflect.TypeOf(&bytes.Buffer{}),
			expected: "*bytes.Buffer",
		},
		{
			name:     "local named type",
			input:    reflect.TypeOf(localNamedType{}),
			expected: "github.com/cloudwego/eino/schema.localNamedType",
		},
		{
			name:     "pointer to local named type",
			input:    reflect.TypeOf(&localNamedType{}),
			expected: "*github.com/cloudwego/eino/schema.localNamedType",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			actual := getTypeName(tc.input)
			if actual != tc.expected {
				t.Errorf("getTypeName() got %q, want %q", actual, tc.expected)
			}
		})
	}
}
