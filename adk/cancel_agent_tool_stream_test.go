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

package adk

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// completedAgentToolModel optionally calls a child and blocks one model call
// after its first chunk. It records model inputs so resume tests can check
// the restored conversation, including the child tool result.
type completedAgentToolModel struct {
	parent    bool
	calls     int32
	blockCall int32
	release   chan struct{}
	inputs    chan []*schema.Message
}

func (*completedAgentToolModel) BindTools([]*schema.ToolInfo) error { return nil }

func (m *completedAgentToolModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	stream, err := m.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return schema.ConcatMessageStream(stream)
}

func (m *completedAgentToolModel) Stream(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	call := atomic.AddInt32(&m.calls, 1)
	if m.inputs != nil {
		snapshot := make([]*schema.Message, len(input))
		for i, msg := range input {
			if msg != nil {
				copied := *msg
				copied.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
				snapshot[i] = &copied
			}
		}
		m.inputs <- snapshot
	}
	if m.parent && call == 1 {
		index := 0
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index: &index, ID: "call_child", Type: "function",
				Function: schema.FunctionCall{Name: "child", Arguments: `{"request":"help"}`},
			}},
		}}), nil
	}
	if call != m.blockCall {
		if !m.parent {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("child done", nil)}), nil
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("resumed parent done", nil)}), nil
	}
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(schema.AssistantMessage("first token", nil), nil)
		<-m.release
	}()
	return reader, nil
}

func cancelAgentToolStream(t *testing.T, iter *AsyncIterator[*AgentEvent], cancel AgentCancelFunc, target string, timeout bool) *CancelError {
	t.Helper()
	var handle *CancelHandle
	var cancelErrors []*CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			var ce *CancelError
			require.ErrorAs(t, event.Err, &ce, "cancellation must not leak a plain node/stream error")
			require.NotNil(t, ce.interruptSignal, "cancellation must carry a resumable interrupt")
			require.Equal(t, timeout, ce.Info.Timeout)
			cancelErrors = append(cancelErrors, ce)
		}
		if event.Output == nil || event.Output.MessageOutput == nil || event.Output.MessageOutput.MessageStream == nil {
			continue
		}
		stream := event.Output.MessageOutput.MessageStream
		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				require.True(t, recvErr == io.EOF || errors.Is(recvErr, ErrStreamCanceled), "%v", recvErr)
				break
			}
			if event.AgentName == target && chunk.Content == "first token" && handle == nil {
				opts := []AgentCancelOption{WithRecursive()}
				if timeout {
					opts = append(opts, WithAgentCancelMode(CancelAfterChatModel), WithAgentCancelTimeout(20*time.Millisecond))
				}
				var contributed bool
				handle, contributed = cancel(opts...)
				require.True(t, contributed)
			}
		}
		stream.Close()
	}
	require.NotNil(t, handle, "did not reach %s's blocked stream", target)
	if timeout {
		require.ErrorIs(t, handle.Wait(), ErrCancelTimeout)
	} else {
		require.NoError(t, handle.Wait())
	}
	require.Len(t, cancelErrors, 1)
	lateHandle, contributed := cancel(WithRecursive())
	require.False(t, contributed, "a finalized cancellation must not accept another request")
	require.NoError(t, lateHandle.Wait())
	return cancelErrors[0]
}

func TestWithCancel_StreamSetupErrorPreservesExecutionError(t *testing.T) {
	ctx := context.Background()
	modelErr := errors.New("stream setup failed")
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent", Description: "agent", Model: &errorChatModel{err: modelErr},
	})
	require.NoError(t, err)
	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, EnableStreaming: true, CheckPointStore: store})
	cancelOpt, cancel := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("help")}, cancelOpt, WithCheckPointID("stream-setup-error"))

	var errorsSeen int
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			require.ErrorIs(t, event.Err, modelErr)
			require.False(t, errors.As(event.Err, new(*CancelError)))
			errorsSeen++
		}
	}
	require.Equal(t, 1, errorsSeen)
	_, saved, err := store.Get(ctx, "stream-setup-error")
	require.NoError(t, err)
	require.False(t, saved, "a model setup error must not produce a cancellation checkpoint")
	handle, contributed := cancel(WithRecursive())
	require.False(t, contributed)
	require.ErrorIs(t, handle.Wait(), ErrExecutionEnded)
}

func drainAgentToolResume(t *testing.T, iter *AsyncIterator[*AgentEvent]) map[string]string {
	t.Helper()
	output := make(map[string]string)
	for {
		event, ok := iter.Next()
		if !ok {
			return output
		}
		require.NoError(t, event.Err)
		if event.Output == nil || event.Output.MessageOutput == nil || event.Output.MessageOutput.MessageStream == nil {
			continue
		}
		stream := event.Output.MessageOutput.MessageStream
		for {
			chunk, recvErr := stream.Recv()
			if recvErr != nil {
				require.ErrorIs(t, recvErr, io.EOF)
				break
			}
			output[event.AgentName] += chunk.Content
		}
		stream.Close()
	}
}

func recordedAgentToolInputs(t *testing.T, m *completedAgentToolModel) [][]*schema.Message {
	t.Helper()
	var inputs [][]*schema.Message
	for i := int32(0); i < atomic.LoadInt32(&m.calls); i++ {
		select {
		case input := <-m.inputs:
			inputs = append(inputs, input)
		default:
			t.Fatal("missing recorded model input")
		}
	}
	return inputs
}

func assertChildToolResult(t *testing.T, input []*schema.Message) {
	t.Helper()
	assistantIndex, toolIndex := -1, -1
	var calls, results int
	for i, msg := range input {
		if msg == nil {
			continue
		}
		for _, call := range msg.ToolCalls {
			if msg.Role == schema.Assistant && call.ID == "call_child" {
				require.Equal(t, "child", call.Function.Name)
				assistantIndex = i
				calls++
			}
		}
		if msg.Role == schema.Tool && msg.ToolCallID == "call_child" {
			require.Equal(t, "child done", msg.Content, "resume lost or changed the child result")
			toolIndex = i
			results++
		}
	}
	require.Equal(t, 1, calls, "resume must preserve the assistant's child tool call")
	require.Equal(t, 1, results, "resume must preserve exactly one matching child result")
	require.Greater(t, toolIndex, assistantIndex, "tool result must follow its tool call")
}

func TestWithCancel_RecursiveAfterAgentToolCompletion_CheckpointsAndResumes(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "Immediate"
		if timeout {
			name = "SafePointTimeout"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			parentModel := &completedAgentToolModel{
				parent: true, blockCall: 2, release: make(chan struct{}), inputs: make(chan []*schema.Message, 4),
			}
			t.Cleanup(func() { close(parentModel.release) })
			childModel := &completedAgentToolModel{}
			child, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name: "child", Description: "child", Model: childModel,
			})
			require.NoError(t, err)
			parent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
				Name: "parent", Description: "parent", Model: parentModel,
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig:    compose.ToolsNodeConfig{Tools: []tool.BaseTool{NewAgentTool(ctx, child)}},
					EmitInternalEvents: true,
				},
			})
			require.NoError(t, err)
			store := newCancelTestStore()
			runner := NewRunner(ctx, RunnerConfig{Agent: parent, EnableStreaming: true, CheckPointStore: store})
			cancelOpt, cancel := WithCancel()
			iter := runner.Run(ctx, []Message{schema.UserMessage("help")}, cancelOpt, WithCheckPointID("completed-child"))

			cancelAgentToolStream(t, iter, cancel, "parent", timeout)
			require.Equal(t, int32(1), atomic.LoadInt32(&childModel.calls))
			_, saved, err := store.Get(ctx, "completed-child")
			require.NoError(t, err)
			require.True(t, saved)

			resumed, err := runner.Resume(ctx, "completed-child")
			require.NoError(t, err)
			output := drainAgentToolResume(t, resumed)
			require.Equal(t, "resumed parent done", output["parent"])
			require.Equal(t, int32(3), atomic.LoadInt32(&parentModel.calls))
			require.Equal(t, int32(1), atomic.LoadInt32(&childModel.calls), "resume repeated a completed child")
			inputs := recordedAgentToolInputs(t, parentModel)
			assertChildToolResult(t, inputs[1])
			assertChildToolResult(t, inputs[2])
		})
	}
}

func TestWithCancel_RecursiveDuringAgentToolStream_ChildInterruptReachesParent(t *testing.T) {
	ctx := context.Background()
	childModel := &completedAgentToolModel{
		blockCall: 1, release: make(chan struct{}), inputs: make(chan []*schema.Message, 4),
	}
	t.Cleanup(func() { close(childModel.release) })
	child, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "child", Description: "child", Model: childModel,
	})
	require.NoError(t, err)
	parentModel := &completedAgentToolModel{parent: true, inputs: make(chan []*schema.Message, 4)}
	parent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "parent", Description: "parent", Model: parentModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig:    compose.ToolsNodeConfig{Tools: []tool.BaseTool{NewAgentTool(ctx, child)}},
			EmitInternalEvents: true,
		},
	})
	require.NoError(t, err)
	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{Agent: parent, EnableStreaming: true, CheckPointStore: store})
	cancelOpt, cancel := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("help")}, cancelOpt, WithCheckPointID("active-child"))

	cancelErr := cancelAgentToolStream(t, iter, cancel, "child", false)
	assertCancelErrorIncludesAddress(t, cancelErr, "child")
	require.Equal(t, int32(1), atomic.LoadInt32(&parentModel.calls), "parent must still be waiting for its child")
	_, saved, err := store.Get(ctx, "active-child")
	require.NoError(t, err)
	require.True(t, saved)

	resumed, err := runner.Resume(ctx, "active-child")
	require.NoError(t, err)
	output := drainAgentToolResume(t, resumed)
	require.Equal(t, "resumed parent done", output["parent"])
	require.Equal(t, "child done", output["child"])
	require.Equal(t, int32(2), atomic.LoadInt32(&parentModel.calls), "resume must not repeat the parent's tool-call model step")
	require.Equal(t, int32(2), atomic.LoadInt32(&childModel.calls))
	parentInputs := recordedAgentToolInputs(t, parentModel)
	assertChildToolResult(t, parentInputs[1])
	childInputs := recordedAgentToolInputs(t, childModel)
	require.Equal(t, childInputs[0], childInputs[1], "child resume must restore the interrupted model input")
}
