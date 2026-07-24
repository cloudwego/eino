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
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestChatModelTurnScriptAndCalls(t *testing.T) {
	m := New().
		OnTurn(0).Reply("hello").
		OnTurn(1).ReplyToolCall("get_weather", `{"city":"sh"}`).
		OnTurn(2).Reply("18C").
		Build()

	first, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.Equal(t, "hello", first.Content)

	secondInput := []*schema.Message{schema.UserMessage("what is the weather")}
	second, err := m.Generate(context.Background(), secondInput)
	require.NoError(t, err)
	require.Len(t, second.ToolCalls, 1)
	require.Equal(t, "get_weather", second.ToolCalls[0].Function.Name)

	toolResult := &schema.Message{Role: schema.Tool, ToolName: "get_weather", Content: "18C"}
	third, err := m.Generate(context.Background(), append(secondInput, second, toolResult))
	require.NoError(t, err)
	require.Equal(t, "18C", third.Content)

	calls := m.Calls()
	require.Len(t, calls, 3)
	require.Equal(t, "what is the weather", calls[1].LastUserMessage())
	require.Equal(t, "get_weather", calls[2].LastToolMessageName())

	secondInput[0].Content = "mutated"
	calls[1].Messages[0].Content = "also mutated"
	require.Equal(t, "what is the weather", m.Calls()[1].LastUserMessage())
}

func TestChatModelPromptMatcherAndStream(t *testing.T) {
	m := New().
		OnPrompt(func(messages []*schema.Message) bool {
			return strings.Contains(messages[len(messages)-1].Content, "stream")
		}).ReplyStream("hel", "lo").
		Build()

	stream, err := m.Stream(context.Background(), []*schema.Message{schema.UserMessage("please stream")})
	require.NoError(t, err)
	first, err := stream.Recv()
	require.NoError(t, err)
	second, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
	stream.Close()
	require.Equal(t, "hel", first.Content)
	require.Equal(t, "lo", second.Content)
	require.Equal(t, CallModeStream, m.Calls()[0].Mode)
}

func TestChatModelStreamScriptConcatenatesForGenerate(t *testing.T) {
	m := New().OnTurn(0).ReplyStream("hel", "lo").Build()

	message, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	require.Equal(t, "hello", message.Content)
}

func TestChatModelWithToolsAndOptions(t *testing.T) {
	m := New().
		OnTurn(0).Reply("base").
		OnTurn(1).Reply("derived").
		Build()
	tool := &schema.ToolInfo{Name: "weather"}

	derived, err := m.WithTools([]*schema.ToolInfo{tool})
	require.NoError(t, err)
	_, err = m.Generate(context.Background(), nil)
	require.NoError(t, err)
	_, err = derived.Generate(context.Background(), nil, model.WithTemperature(0.2))
	require.NoError(t, err)

	calls := m.Calls()
	require.Empty(t, calls[0].Tools)
	require.Equal(t, "weather", calls[1].Tools[0].Name)
	require.Equal(t, float32(0.2), *calls[1].Options.Temperature)
	tool.Name = "mutated"
	require.Equal(t, "weather", m.Calls()[1].Tools[0].Name)
}

func TestChatModelErrors(t *testing.T) {
	scriptedErr := errors.New("scripted")
	m := New().OnTurn(0).ReplyError(scriptedErr).Build()

	_, err := m.Generate(context.Background(), nil)
	require.ErrorIs(t, err, scriptedErr)
	_, err = m.Generate(context.Background(), nil)
	require.ErrorContains(t, err, "no scripted response matched turn 1")
}

func TestChatModelConcurrentCalls(t *testing.T) {
	const callCount = 32
	builder := New()
	for i := 0; i < callCount; i++ {
		builder.OnTurn(i).Reply(strconv.Itoa(i))
	}
	m := builder.Build()

	var wg sync.WaitGroup
	errs := make(chan error, callCount)
	for i := 0; i < callCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Generate(context.Background(), nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	require.Len(t, m.Calls(), callCount)
}
