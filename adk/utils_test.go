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
	"sync"
	"testing"
	"time"
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
