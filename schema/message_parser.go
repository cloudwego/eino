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
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
)

// MessageParser parses a Message into a strongly typed value.
type MessageParser[T any] interface {
	Parse(ctx context.Context, m *Message) (T, error)
}

// MessageParseFrom determines the source of the data to be parsed. default is content (Message.Content).
type MessageParseFrom string

// MessageParseFrom indicates the source data used by the parser.
const (
	MessageParseFromContent  MessageParseFrom = "content"
	MessageParseFromToolCall MessageParseFrom = "tool_call"
)

// MessageJSONParseConfig configures JSON parsing behavior for Message.
type MessageJSONParseConfig struct {
	// parse from content or tool call, default is content.
	ParseFrom MessageParseFrom `json:"parse_from,omitempty"`

	// parse key path, default is empty.
	// must be a valid json path expression, eg: field.sub_field
	ParseKeyPath string `json:"parse_key_path,omitempty"`
}

// NewMessageJSONParser creates a new MessageJSONParser.
func NewMessageJSONParser[T any](config *MessageJSONParseConfig) MessageParser[T] {
	if config == nil {
		config = &MessageJSONParseConfig{}
	}

	if config.ParseFrom == "" {
		config.ParseFrom = MessageParseFromContent
	}

	return &MessageJSONParser[T]{
		ParseFrom:    config.ParseFrom,
		ParseKeyPath: config.ParseKeyPath,
	}
}

// MessageJSONParser is a parser that parses a message into an object T, using json unmarshal.
// eg of parse to single struct:
//
//	config := &MessageJSONParseConfig{
//		ParseFrom: MessageParseFromToolCall,
//	}
//	parser := NewMessageJSONParser[GetUserParam](config)
//	param, err := parser.Parse(ctx, message)
//
//	eg of parse to slice of struct:
//
//	config := &MessageJSONParseConfig{
//		ParseFrom: MessageParseFromToolCall,
//	}
//
// parser := NewMessageJSONParser[GetUserParam](config)
// param, err := parser.Parse(ctx, message)
type MessageJSONParser[T any] struct {
	ParseFrom    MessageParseFrom
	ParseKeyPath string
}

// Parse parses a message into an object T.
func (p *MessageJSONParser[T]) Parse(ctx context.Context, m *Message) (parsed T, err error) {
	if p.ParseFrom == MessageParseFromContent {
		return p.parse(m.Content)
	} else if p.ParseFrom == MessageParseFromToolCall {
		if len(m.ToolCalls) == 0 {
			return parsed, fmt.Errorf("no tool call found")
		}

		return p.parse(m.ToolCalls[0].Function.Arguments)
	}

	return parsed, fmt.Errorf("invalid parse from type: %s", p.ParseFrom)
}

// extractData extracts data from a string using the parse key path.
func (p *MessageJSONParser[T]) extractData(data string) (string, error) {
	if p.ParseKeyPath == "" {
		return data, nil
	}

	keys := strings.Split(p.ParseKeyPath, ".")
	interfaceKeys := make([]any, len(keys))
	for i, key := range keys {
		interfaceKeys[i] = key
	}

	node, err := sonic.GetFromString(data, interfaceKeys...)
	if err != nil {
		return "", fmt.Errorf("failed to get parse key path: %w", err)
	}

	bytes, err := node.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("failed to marshal node: %w", err)
	}

	return string(bytes), nil
}

// parse parses a string into an object T.
func (p *MessageJSONParser[T]) parse(data string) (parsed T, err error) {
	parsedData, err := p.extractData(data)
	if err != nil {
		return parsed, err
	}

	if err := sonic.UnmarshalString(parsedData, &parsed); err != nil {
		return parsed, fmt.Errorf("failed to unmarshal content: %w", err)
	}

	return parsed, nil
}
