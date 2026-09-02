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

package modeltest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// PromptMatcher reports whether a scripted response applies to a prompt.
type PromptMatcher func(messages []*schema.Message) bool

// CallMode identifies the ChatModel method used for a recorded call.
type CallMode string

const (
	// CallModeGenerate identifies a Generate call.
	CallModeGenerate CallMode = "generate"
	// CallModeStream identifies a Stream call.
	CallModeStream CallMode = "stream"
)

// Call is an immutable snapshot of one model invocation.
type Call struct {
	Turn     int
	Mode     CallMode
	Messages []*schema.Message
	Tools    []*schema.ToolInfo
	Options  model.Options
}

// LastUserMessage returns the content of the last user message, or an empty string.
func (c Call) LastUserMessage() string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i] != nil && c.Messages[i].Role == schema.User {
			return c.Messages[i].Content
		}
	}
	return ""
}

// LastToolMessageName returns the name of the last tool result message, or an empty string.
func (c Call) LastToolMessageName() string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i] != nil && c.Messages[i].Role == schema.Tool {
			return c.Messages[i].ToolName
		}
	}
	return ""
}

// Builder constructs a scripted ChatModel.
type Builder struct {
	expectations   []*expectation
	nextToolCallID int
}

// New creates an empty scripted ChatModel builder.
func New() *Builder {
	return &Builder{}
}

// OnTurn selects a response by zero-based model call number.
func (b *Builder) OnTurn(turn int) *ResponseBuilder {
	e := &expectation{turn: &turn}
	b.expectations = append(b.expectations, e)
	return &ResponseBuilder{parent: b, expectation: e}
}

// OnPrompt selects the next unused response whose matcher accepts the prompt.
func (b *Builder) OnPrompt(matcher PromptMatcher) *ResponseBuilder {
	e := &expectation{matcher: matcher}
	b.expectations = append(b.expectations, e)
	return &ResponseBuilder{parent: b, expectation: e}
}

// Build creates a concurrency-safe scripted ChatModel.
func (b *Builder) Build() *ChatModel {
	expectations := make([]*expectation, len(b.expectations))
	for i, source := range b.expectations {
		cloned := *source
		cloned.response.messages = cloneMessages(source.response.messages)
		expectations[i] = &cloned
	}
	return &ChatModel{
		state: &scriptState{
			expectations: expectations,
		},
	}
}

// ResponseBuilder configures one scripted response.
type ResponseBuilder struct {
	parent      *Builder
	expectation *expectation
}

// Reply configures a text assistant response.
func (b *ResponseBuilder) Reply(content string) *Builder {
	return b.ReplyMessage(schema.AssistantMessage(content, nil))
}

// ReplyMessage configures a complete response message.
func (b *ResponseBuilder) ReplyMessage(message *schema.Message) *Builder {
	b.expectation.response.messages = []*schema.Message{cloneMessage(message)}
	b.expectation.response.configured = true
	return b.parent
}

// ReplyToolCall configures a single assistant tool call.
func (b *ResponseBuilder) ReplyToolCall(name, arguments string) *Builder {
	id := fmt.Sprintf("call_%d", b.parent.nextToolCallID)
	b.parent.nextToolCallID++
	return b.ReplyMessage(&schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   id,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: arguments,
			},
		}},
	})
}

// ReplyStream configures assistant text chunks for Stream calls.
//
// Generate concatenates the same chunks into one complete message.
func (b *ResponseBuilder) ReplyStream(chunks ...string) *Builder {
	messages := make([]*schema.Message, len(chunks))
	for i, chunk := range chunks {
		messages[i] = schema.AssistantMessage(chunk, nil)
	}
	return b.ReplyStreamMessages(messages...)
}

// ReplyStreamMessages configures message chunks for Stream calls.
//
// Generate concatenates the same chunks into one complete message.
func (b *ResponseBuilder) ReplyStreamMessages(chunks ...*schema.Message) *Builder {
	b.expectation.response.messages = cloneMessages(chunks)
	b.expectation.response.configured = true
	return b.parent
}

// ReplyError configures an error response for Generate and Stream.
func (b *ResponseBuilder) ReplyError(err error) *Builder {
	if err == nil {
		err = errors.New("modeltest: scripted nil error")
	}
	b.expectation.response.err = err
	b.expectation.response.configured = true
	return b.parent
}

// ChatModel is a concurrency-safe scripted implementation of
// model.BaseChatModel and model.ToolCallingChatModel.
type ChatModel struct {
	state *scriptState
	tools []*schema.ToolInfo
}

// Generate returns the matching scripted response.
func (m *ChatModel) Generate(_ context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	response, err := m.next(CallModeGenerate, input, opts)
	if err != nil {
		return nil, err
	}
	if len(response.messages) == 1 {
		return cloneMessage(response.messages[0]), nil
	}
	return schema.ConcatMessages(cloneMessages(response.messages))
}

// Stream returns the matching scripted response as a message stream.
func (m *ChatModel) Stream(_ context.Context, input []*schema.Message,
	opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	response, err := m.next(CallModeStream, input, opts)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray(cloneMessages(response.messages)), nil
}

// WithTools returns a derived model with an immutable bound tool list.
//
// The derived model shares the script and call history with its parent.
func (m *ChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &ChatModel{
		state: m.state,
		tools: cloneToolInfos(tools),
	}, nil
}

// Calls returns defensive copies of all recorded calls.
func (m *ChatModel) Calls() []Call {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()

	calls := make([]Call, len(m.state.calls))
	for i := range m.state.calls {
		calls[i] = cloneCall(m.state.calls[i])
	}
	return calls
}

func (m *ChatModel) next(mode CallMode, input []*schema.Message, opts []model.Option) (scriptedResponse, error) {
	options := model.GetCommonOptions(&model.Options{Tools: cloneToolInfos(m.tools)}, opts...)
	call := Call{
		Mode:     mode,
		Messages: cloneMessages(input),
		Tools:    cloneToolInfos(options.Tools),
		Options:  cloneOptions(*options),
	}

	m.state.mu.Lock()
	defer m.state.mu.Unlock()

	call.Turn = len(m.state.calls)
	m.state.calls = append(m.state.calls, call)

	for _, e := range m.state.expectations {
		if e.used || !e.matches(call.Turn, input) {
			continue
		}
		e.used = true
		if !e.response.configured {
			return scriptedResponse{}, fmt.Errorf("modeltest: response for turn %d is not configured", call.Turn)
		}
		if e.response.err != nil {
			return scriptedResponse{}, e.response.err
		}
		return scriptedResponse{messages: cloneMessages(e.response.messages), configured: true}, nil
	}
	return scriptedResponse{}, fmt.Errorf("modeltest: no scripted response matched turn %d", call.Turn)
}

type scriptState struct {
	mu           sync.Mutex
	expectations []*expectation
	calls        []Call
}

type expectation struct {
	turn     *int
	matcher  PromptMatcher
	response scriptedResponse
	used     bool
}

func (e *expectation) matches(turn int, messages []*schema.Message) bool {
	if e.turn != nil {
		return *e.turn == turn
	}
	return e.matcher != nil && e.matcher(cloneMessages(messages))
}

type scriptedResponse struct {
	messages   []*schema.Message
	err        error
	configured bool
}

func cloneCall(call Call) Call {
	call.Messages = cloneMessages(call.Messages)
	call.Tools = cloneToolInfos(call.Tools)
	call.Options = cloneOptions(call.Options)
	return call
}

func cloneOptions(options model.Options) model.Options {
	cloned := options
	cloned.Temperature = clonePointer(options.Temperature)
	cloned.Model = clonePointer(options.Model)
	cloned.TopP = clonePointer(options.TopP)
	cloned.MaxTokens = clonePointer(options.MaxTokens)
	cloned.ToolChoice = clonePointer(options.ToolChoice)
	cloned.AgenticToolChoice = clonePointer(options.AgenticToolChoice)
	cloned.Stop = append([]string(nil), options.Stop...)
	cloned.AllowedToolNames = append([]string(nil), options.AllowedToolNames...)
	cloned.Tools = cloneToolInfos(options.Tools)
	cloned.DeferredTools = cloneToolInfos(options.DeferredTools)
	cloned.ToolSearchTool = cloneToolInfo(options.ToolSearchTool)
	return cloned
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneMessages(messages []*schema.Message) []*schema.Message {
	cloned := make([]*schema.Message, len(messages))
	for i, message := range messages {
		cloned[i] = cloneMessage(message)
	}
	return cloned
}

func cloneMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	data, err := json.Marshal(message)
	if err == nil {
		cloned := &schema.Message{}
		if json.Unmarshal(data, cloned) == nil {
			return cloned
		}
	}
	cloned := *message
	return &cloned
}

func cloneToolInfos(tools []*schema.ToolInfo) []*schema.ToolInfo {
	cloned := make([]*schema.ToolInfo, len(tools))
	for i, tool := range tools {
		cloned[i] = cloneToolInfo(tool)
	}
	return cloned
}

func cloneToolInfo(tool *schema.ToolInfo) *schema.ToolInfo {
	if tool == nil {
		return nil
	}
	data, err := json.Marshal(tool)
	if err == nil {
		cloned := &schema.ToolInfo{}
		if json.Unmarshal(data, cloned) == nil {
			return cloned
		}
	}
	cloned := *tool
	return &cloned
}

var (
	_ model.BaseChatModel        = (*ChatModel)(nil)
	_ model.ToolCallingChatModel = (*ChatModel)(nil)
)
