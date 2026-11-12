/*
 * Copyright 2024 CloudWeGo Authors
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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

type TestStructForParse struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	XX   struct {
		YY int `json:"yy"`
	} `json:"xx"`
}

func TestMessageJSONParser(t *testing.T) {
	ctx := context.Background()

	t.Run("parse from content", func(t *testing.T) {
		parser := NewMessageJSONParser[TestStructForParse](&MessageJSONParseConfig{
			ParseFrom: MessageParseFromContent,
		})

		parsed, err := parser.Parse(ctx, &Message{
			Content: `{"id": 1, "name": "test", "xx": {"yy": 2}}`,
		})
		assert.Nil(t, err)
		assert.Equal(t, 1, parsed.ID)
	})

	t.Run("default source leaves the config unchanged", func(t *testing.T) {
		config := &MessageJSONParseConfig{ParseKeyPath: "id"}
		parser := NewMessageJSONParser[int](config)

		parsed, err := parser.Parse(ctx, &Message{Content: `{"id": 7}`})
		assert.NoError(t, err)
		assert.Equal(t, 7, parsed)
		assert.Equal(t, &MessageJSONParseConfig{ParseKeyPath: "id"}, config)
	})

	t.Run("parse from tool call", func(t *testing.T) {
		t.Run("only one tool call, default use first tool call", func(t *testing.T) {
			parser := NewMessageJSONParser[TestStructForParse](&MessageJSONParseConfig{
				ParseFrom: MessageParseFromToolCall,
			})

			parsed, err := parser.Parse(ctx, &Message{
				ToolCalls: []ToolCall{
					{Function: FunctionCall{Arguments: `{"id": 1, "name": "test", "xx": {"yy": 2}}`}},
				},
			})
			assert.Nil(t, err)
			assert.Equal(t, 1, parsed.ID)
		})

		t.Run("parse key path", func(t *testing.T) {
			type TestStructForParse2 struct {
				YY int `json:"yy"`
			}

			parser := NewMessageJSONParser[TestStructForParse2](&MessageJSONParseConfig{
				ParseFrom:    MessageParseFromToolCall,
				ParseKeyPath: "xx",
			})

			parsed, err := parser.Parse(ctx, &Message{
				ToolCalls: []ToolCall{
					{Function: FunctionCall{Arguments: `{"id": 1, "name": "test", "xx": {"yy": 2}}`}},
				},
			})
			assert.Nil(t, err)
			assert.Equal(t, 2, parsed.YY)
		})

		t.Run("parse key path, deep level", func(t *testing.T) {
			type TestStructForParse3 struct {
				ZZ int `json:"zz"`
			}

			parser := NewMessageJSONParser[TestStructForParse3](&MessageJSONParseConfig{
				ParseFrom:    MessageParseFromToolCall,
				ParseKeyPath: "xx.yy",
			})

			parsed, err := parser.Parse(ctx, &Message{
				ToolCalls: []ToolCall{
					{Function: FunctionCall{Arguments: `{"id": 1, "name": "test", "xx": {"yy": {"zz": 3}}}`}},
				},
			})
			assert.Nil(t, err)
			assert.Equal(t, 3, parsed.ZZ)
		})

		t.Run("parse key with pointer", func(t *testing.T) {
			type TestStructForParse4 struct {
				ZZ *int `json:"zz"`
			}

			parser := NewMessageJSONParser[**TestStructForParse4](&MessageJSONParseConfig{
				ParseFrom: MessageParseFromToolCall,
			})

			parsed, err := parser.Parse(ctx, &Message{
				ToolCalls: []ToolCall{{Function: FunctionCall{Arguments: `{"zz": 3}`}}},
			})
			assert.Nil(t, err)
			assert.Equal(t, 3, *((**parsed).ZZ))
		})
	})

	t.Run("parse of slice", func(t *testing.T) {
		t.Run("valid slice string, not multiple tool calls", func(t *testing.T) {
			parser := NewMessageJSONParser[[]map[string]any](&MessageJSONParseConfig{
				ParseFrom: MessageParseFromToolCall,
			})

			parsed, err := parser.Parse(ctx, &Message{
				ToolCalls: []ToolCall{{Function: FunctionCall{Arguments: `[{"id": 1}, {"id": 2}]`}}},
			})
			assert.Nil(t, err)
			assert.Equal(t, 2, len(parsed))
		})

		t.Run("invalid slice string, not multiple tool calls", func(t *testing.T) {
			parser := NewMessageJSONParser[[]map[string]any](&MessageJSONParseConfig{
				ParseFrom: MessageParseFromToolCall,
			})

			_, err := parser.Parse(ctx, &Message{
				ToolCalls: []ToolCall{
					{Function: FunctionCall{Arguments: `{"id": 1}`}},
					{Function: FunctionCall{Arguments: `{"id": 2}`}},
				},
			})
			assert.NotNil(t, err)
		})
	})

	t.Run("invalid configs", func(t *testing.T) {
		parser := NewMessageJSONParser[TestStructForParse](nil)
		_, err := parser.Parse(ctx, &Message{
			Content: "",
		})
		assert.NotNil(t, err)
	})

	t.Run("invalid parse key path", func(t *testing.T) {
		parser := NewMessageJSONParser[TestStructForParse](&MessageJSONParseConfig{
			ParseKeyPath: "...invalid",
		})
		_, err := parser.Parse(ctx, &Message{})
		assert.NotNil(t, err)
	})

	t.Run("invalid parse from", func(t *testing.T) {
		parser := NewMessageJSONParser[TestStructForParse](&MessageJSONParseConfig{
			ParseFrom: "invalid",
		})
		_, err := parser.Parse(ctx, &Message{})
		assert.NotNil(t, err)
	})

	t.Run("invalid parse from type", func(t *testing.T) {
		parser := NewMessageJSONParser[int](&MessageJSONParseConfig{
			ParseFrom: MessageParseFrom("invalid"),
		})
		_, err := parser.Parse(ctx, &Message{})
		assert.NotNil(t, err)
	})

}
