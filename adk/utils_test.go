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

package adk

import (
	"bytes"
	"encoding/gob"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestAsyncIteratorPair_Basic(t *testing.T) {
	// Create a new iterator-generator pair
	iterator, generator := NewAsyncIteratorPair[string]()

	// Test sending and receiving a value
	generator.Send("test1")
	val, ok := iterator.Next()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != "test1" {
		t.Errorf("expected 'test1', got '%s'", val)
	}

	// Test sending and receiving multiple values
	generator.Send("test2")
	generator.Send("test3")

	val, ok = iterator.Next()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != "test2" {
		t.Errorf("expected 'test2', got '%s'", val)
	}

	val, ok = iterator.Next()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != "test3" {
		t.Errorf("expected 'test3', got '%s'", val)
	}
}

func TestAsyncIteratorPair_Close(t *testing.T) {
	iterator, generator := NewAsyncIteratorPair[int]()

	// Send some values
	generator.Send(1)
	generator.Send(2)

	// Close the generator
	generator.Close()

	// Should still be able to read existing values
	val, ok := iterator.Next()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != 1 {
		t.Errorf("expected 1, got %d", val)
	}

	val, ok = iterator.Next()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != 2 {
		t.Errorf("expected 2, got %d", val)
	}

	// After consuming all values, Next should return false
	_, ok = iterator.Next()
	if ok {
		t.Error("receive from closed, empty channel should return ok=false")
	}
}

func TestAsyncIteratorPair_Concurrency(t *testing.T) {
	iterator, generator := NewAsyncIteratorPair[int]()
	const numSenders = 5
	const numReceivers = 3
	const messagesPerSender = 100

	var rwg, swg sync.WaitGroup
	rwg.Add(numReceivers)
	swg.Add(numSenders)

	// Start senders
	for i := 0; i < numSenders; i++ {
		go func(id int) {
			defer swg.Done()
			for j := 0; j < messagesPerSender; j++ {
				generator.Send(id*messagesPerSender + j)
				time.Sleep(time.Microsecond) // Small delay to increase concurrency chance
			}
		}(i)
	}

	// Start receivers
	received := make([]int, 0, numSenders*messagesPerSender)
	var mu sync.Mutex

	for i := 0; i < numReceivers; i++ {
		go func() {
			defer rwg.Done()
			for {
				val, ok := iterator.Next()
				if !ok {
					return
				}
				mu.Lock()
				received = append(received, val)
				mu.Unlock()
			}
		}()
	}

	// Wait for senders to finish
	swg.Wait()
	generator.Close()

	// Wait for all goroutines to finish
	rwg.Wait()

	// Verify we received all messages
	if len(received) != numSenders*messagesPerSender {
		t.Errorf("expected %d messages, got %d", numSenders*messagesPerSender, len(received))
	}

	// Create a map to check for duplicates and missing values
	receivedMap := make(map[int]bool)
	for _, val := range received {
		receivedMap[val] = true
	}

	if len(receivedMap) != numSenders*messagesPerSender {
		t.Error("duplicate or missing messages detected")
	}
}

func TestGenErrorIter(t *testing.T) {
	iter := genErrorIter(fmt.Errorf("test"))
	e, ok := iter.Next()
	assert.True(t, ok)
	assert.Equal(t, "test", e.Err.Error())
	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestGetMessageFromWrappedEvent_StreamError_MultipleCallsGuard(t *testing.T) {
	streamErr := errors.New("stream error")

	sr, sw := schema.Pipe[Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("chunk1", nil), nil)
		sw.Send(schema.AssistantMessage("chunk2", nil), nil)
		sw.Send(nil, streamErr)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: sr,
				},
			},
		},
	}

	msg1, err1 := getMessageFromWrappedEvent(wrapper)
	assert.Nil(t, msg1)
	assert.NotNil(t, err1)
	assert.Equal(t, "stream error", err1.Error())

	assert.NotEmpty(t, wrapper.StreamErr)
	assert.Equal(t, err1, wrapper.StreamErr)

	msg2, err2 := getMessageFromWrappedEvent(wrapper)
	assert.Nil(t, msg2)
	assert.Equal(t, err1, err2)
}

func TestGetMessageFromWrappedEvent_StreamSuccess_MultipleCallsCached(t *testing.T) {
	sr, sw := schema.Pipe[Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("chunk1", nil), nil)
		sw.Send(schema.AssistantMessage("chunk2", nil), nil)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: sr,
				},
			},
		},
	}

	msg1, err1 := getMessageFromWrappedEvent(wrapper)
	assert.NotNil(t, msg1)
	assert.Nil(t, err1)
	assert.Equal(t, "chunk1chunk2", msg1.Content)

	assert.NotNil(t, wrapper.concatenatedMessage)

	msg2, err2 := getMessageFromWrappedEvent(wrapper)
	assert.NotNil(t, msg2)
	assert.Nil(t, err2)
	assert.Equal(t, "chunk1chunk2", msg2.Content)
	assert.Same(t, msg1, msg2)
}

func TestGetMessageFromWrappedEvent_StreamError_PartialMessagesPreserved(t *testing.T) {
	streamErr := errors.New("stream error at chunk3")

	sr, sw := schema.Pipe[Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("chunk1", nil), nil)
		sw.Send(schema.AssistantMessage("chunk2", nil), nil)
		sw.Send(nil, streamErr)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: sr,
				},
			},
		},
	}

	_, err := getMessageFromWrappedEvent(wrapper)
	assert.NotNil(t, err)
	assert.Equal(t, streamErr, wrapper.StreamErr)

	newStream := wrapper.AgentEvent.Output.MessageOutput.MessageStream
	assert.NotNil(t, newStream)

	var msgs []Message
	for {
		msg, err := newStream.Recv()
		if err != nil {
			break
		}
		msgs = append(msgs, msg)
	}

	assert.Equal(t, 2, len(msgs))
	assert.Equal(t, "chunk1", msgs[0].Content)
	assert.Equal(t, "chunk2", msgs[1].Content)
}

func TestAgentEventWrapper_GobEncoding_WithWillRetryError(t *testing.T) {
	streamErr := &WillRetryError{ErrStr: "stream error", RetryAttempt: 2}

	sr, sw := schema.Pipe[Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("partial1", nil), nil)
		sw.Send(schema.AssistantMessage("partial2", nil), nil)
		sw.Send(nil, streamErr)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: sr,
				},
			},
		},
		TS: 12345,
	}

	_, err := getMessageFromWrappedEvent(wrapper)
	assert.NotNil(t, err)
	var wrapperErr *WillRetryError
	assert.True(t, errors.As(wrapper.StreamErr, &wrapperErr))
	assert.Equal(t, streamErr.ErrStr, wrapperErr.ErrStr)
	assert.Equal(t, streamErr.RetryAttempt, wrapperErr.RetryAttempt)

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err = enc.Encode(wrapper)
	assert.NoError(t, err)

	var decoded agentEventWrapper
	dec := gob.NewDecoder(&buf)
	err = dec.Decode(&decoded)
	assert.NoError(t, err)

	assert.Equal(t, "TestAgent", decoded.AgentName)
	assert.Equal(t, int64(12345), decoded.TS)
	var decodedErr *WillRetryError
	assert.True(t, errors.As(decoded.StreamErr, &decodedErr))
	assert.Equal(t, streamErr.ErrStr, decodedErr.ErrStr)
	assert.Equal(t, streamErr.RetryAttempt, decodedErr.RetryAttempt)
}

func TestAgentEventWrapper_GobEncoding_WithUnregisteredError(t *testing.T) {
	streamErr := errors.New("unregistered error type")

	sr, sw := schema.Pipe[Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("partial1", nil), nil)
		sw.Send(nil, streamErr)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: sr,
				},
			},
		},
		TS: 22222,
	}

	_, err := getMessageFromWrappedEvent(wrapper)
	assert.NotNil(t, err)
	assert.Equal(t, streamErr, wrapper.StreamErr)

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err = enc.Encode(wrapper)
	assert.Error(t, err, "gob encoding should fail for unregistered error types")
}

func TestAgentEventWrapper_GobEncoding_WithStreamSuccess(t *testing.T) {
	sr, sw := schema.Pipe[Message](10)
	go func() {
		defer sw.Close()
		sw.Send(schema.AssistantMessage("success1", nil), nil)
		sw.Send(schema.AssistantMessage("success2", nil), nil)
	}()

	wrapper := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			AgentName: "TestAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: sr,
				},
			},
		},
		TS: 67890,
	}

	msg, err := getMessageFromWrappedEvent(wrapper)
	assert.NoError(t, err)
	assert.Equal(t, "success1success2", msg.Content)

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err = enc.Encode(wrapper)
	assert.NoError(t, err)

	var decoded agentEventWrapper
	dec := gob.NewDecoder(&buf)
	err = dec.Decode(&decoded)
	assert.NoError(t, err)

	assert.Equal(t, "TestAgent", decoded.AgentName)
	assert.Equal(t, int64(67890), decoded.TS)
	assert.Empty(t, decoded.StreamErr)
}
