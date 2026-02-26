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
	"sync"
	"testing"
	"time"
)

func TestUnboundedChan_Send(t *testing.T) {
	ch := NewUnboundedChan[string]()

	// Test sending a value
	ch.Send("test")
	if len(ch.buffer) != 1 {
		t.Errorf("buffer length should be 1, got %d", len(ch.buffer))
	}
	if ch.buffer[0] != "test" {
		t.Errorf("expected 'test', got '%s'", ch.buffer[0])
	}

	// Test sending multiple values
	ch.Send("test2")
	ch.Send("test3")
	if len(ch.buffer) != 3 {
		t.Errorf("buffer length should be 3, got %d", len(ch.buffer))
	}
}

func TestUnboundedChan_SendPanic(t *testing.T) {
	ch := NewUnboundedChan[int]()
	ch.Close()

	// Test sending to closed channel should panic
	defer func() {
		if r := recover(); r == nil {
			t.Error("sending to closed channel should panic")
		}
	}()

	ch.Send(1)
}

func TestUnboundedChan_Receive(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// Send values
	ch.Send(1)
	ch.Send(2)

	// Test receiving values
	val, ok := ch.Receive()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != 1 {
		t.Errorf("expected 1, got %d", val)
	}

	val, ok = ch.Receive()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != 2 {
		t.Errorf("expected 2, got %d", val)
	}
}

func TestUnboundedChan_ReceiveFromClosed(t *testing.T) {
	ch := NewUnboundedChan[int]()
	ch.Close()

	// Test receiving from closed, empty channel
	val, ok := ch.Receive()
	if ok {
		t.Error("receive from closed, empty channel should return ok=false")
	}
	if val != 0 {
		t.Errorf("expected zero value, got %d", val)
	}

	// Test receiving from closed channel with values
	ch = NewUnboundedChan[int]()
	ch.Send(42)
	ch.Close()

	val, ok = ch.Receive()
	if !ok {
		t.Error("receive should succeed")
	}
	if val != 42 {
		t.Errorf("expected 42, got %d", val)
	}

	// After consuming all values
	val, ok = ch.Receive()
	if ok {
		t.Error("receive from closed, empty channel should return ok=false")
	}
}

func TestUnboundedChan_Close(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// Test closing
	ch.Close()
	if !ch.closed {
		t.Error("channel should be marked as closed")
	}

	// Test double closing (should not panic)
	ch.Close()
}

func TestUnboundedChan_Concurrency(t *testing.T) {
	ch := NewUnboundedChan[int]()
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
				ch.Send(id*messagesPerSender + j)
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
				val, ok := ch.Receive()
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
	ch.Close()

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

func TestUnboundedChan_BlockingReceive(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// Test that Receive blocks when channel is empty
	receiveDone := make(chan bool)
	go func() {
		ch.Receive()
		receiveDone <- true
	}()

	// Check that receive is blocked
	select {
	case <-receiveDone:
		t.Error("Receive should block on empty channel")
	case <-time.After(50 * time.Millisecond):
		// This is expected
	}

	// Send a value to unblock
	ch.Send(1)

	// Now receive should complete
	select {
	case <-receiveDone:
		// This is expected
	case <-time.After(50 * time.Millisecond):
		t.Error("Receive should have unblocked")
	}
}

func TestUnboundedChan_TakeAll(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// Test TakeAll on empty channel
	items := ch.TakeAll()
	if items != nil {
		t.Errorf("TakeAll on empty channel should return nil, got %v", items)
	}

	// Send some values
	ch.Send(1)
	ch.Send(2)
	ch.Send(3)

	// Test TakeAll returns all values
	items = ch.TakeAll()
	if len(items) != 3 {
		t.Errorf("expected 3 values, got %d", len(items))
	}
	if items[0] != 1 || items[1] != 2 || items[2] != 3 {
		t.Errorf("unexpected values: %v", items)
	}

	// Channel should be empty now
	if len(ch.buffer) != 0 {
		t.Errorf("channel should be empty after TakeAll, got %d values", len(ch.buffer))
	}

	// TakeAll again should return nil
	items = ch.TakeAll()
	if items != nil {
		t.Errorf("TakeAll on empty channel should return nil, got %v", items)
	}
}

func TestUnboundedChan_TakeAll_Partial(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// Send values
	ch.Send(1)
	ch.Send(2)
	ch.Send(3)

	// Receive one
	val, ok := ch.Receive()
	if !ok || val != 1 {
		t.Errorf("expected (1, true), got (%d, %v)", val, ok)
	}

	// TakeAll should return remaining values
	items := ch.TakeAll()
	if len(items) != 2 {
		t.Errorf("expected 2 values, got %d", len(items))
	}
	if items[0] != 2 || items[1] != 3 {
		t.Errorf("unexpected values: %v", items)
	}
}

func TestUnboundedChan_PushFront(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// Test PushFront with empty values (should do nothing)
	ch.PushFront(nil)
	ch.PushFront([]int{})
	if len(ch.buffer) != 0 {
		t.Errorf("PushFront with empty values should not add anything, got %d values", len(ch.buffer))
	}

	// Send some values
	ch.Send(3)
	ch.Send(4)

	// PushFront should prepend values
	ch.PushFront([]int{1, 2})

	if len(ch.buffer) != 4 {
		t.Errorf("expected 4 values, got %d", len(ch.buffer))
	}
	if ch.buffer[0] != 1 || ch.buffer[1] != 2 || ch.buffer[2] != 3 || ch.buffer[3] != 4 {
		t.Errorf("unexpected buffer: %v", ch.buffer)
	}

	// Receive should return in correct order
	val, _ := ch.Receive()
	if val != 1 {
		t.Errorf("expected 1, got %d", val)
	}
	val, _ = ch.Receive()
	if val != 2 {
		t.Errorf("expected 2, got %d", val)
	}
}

func TestUnboundedChan_PushFront_EmptyChannel(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// PushFront to empty channel
	ch.PushFront([]int{1, 2, 3})

	if len(ch.buffer) != 3 {
		t.Errorf("expected 3 values, got %d", len(ch.buffer))
	}

	// Receive should work
	val, ok := ch.Receive()
	if !ok || val != 1 {
		t.Errorf("expected (1, true), got (%d, %v)", val, ok)
	}
}

func TestUnboundedChan_PushFront_UnblocksReceive(t *testing.T) {
	ch := NewUnboundedChan[int]()

	// Start a blocking receive
	receiveDone := make(chan int)
	go func() {
		val, _ := ch.Receive()
		receiveDone <- val
	}()

	// Ensure receive is blocked
	select {
	case <-receiveDone:
		t.Error("Receive should block on empty channel")
	case <-time.After(50 * time.Millisecond):
		// This is expected
	}

	// PushFront should unblock the receive
	ch.PushFront([]int{42})

	select {
	case val := <-receiveDone:
		if val != 42 {
			t.Errorf("expected 42, got %d", val)
		}
	case <-time.After(50 * time.Millisecond):
		t.Error("Receive should have unblocked after PushFront")
	}
}

func TestUnboundedChan_TakeAll_PushFront_Concurrent(t *testing.T) {
	ch := NewUnboundedChan[int]()
	const numOps = 100

	var wg sync.WaitGroup
	wg.Add(3)

	// Goroutine 1: Send values
	go func() {
		defer wg.Done()
		for i := 0; i < numOps; i++ {
			ch.Send(i)
			time.Sleep(time.Microsecond)
		}
	}()

	// Goroutine 2: TakeAll periodically
	takeAllResults := make([][]int, 0)
	var mu sync.Mutex
	go func() {
		defer wg.Done()
		for i := 0; i < numOps/10; i++ {
			items := ch.TakeAll()
			if items != nil {
				mu.Lock()
				takeAllResults = append(takeAllResults, items)
				mu.Unlock()
			}
			time.Sleep(10 * time.Microsecond)
		}
	}()

	// Goroutine 3: PushFront periodically
	go func() {
		defer wg.Done()
		for i := 0; i < numOps/10; i++ {
			ch.PushFront([]int{-i})
			time.Sleep(10 * time.Microsecond)
		}
	}()

	wg.Wait()
	ch.Close()

	// Drain remaining values
	remaining := ch.TakeAll()
	if remaining != nil {
		mu.Lock()
		takeAllResults = append(takeAllResults, remaining)
		mu.Unlock()
	}

	// Count total values collected
	total := 0
	for _, batch := range takeAllResults {
		total += len(batch)
	}

	// We should have exactly numOps (from Send) + numOps/10 (from PushFront) values
	expected := numOps + numOps/10
	if total != expected {
		t.Errorf("expected %d values, got %d", expected, total)
	}
}
