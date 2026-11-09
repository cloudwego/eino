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

package schema_test

import (
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// Closing a merged reader must propagate to the sources it forwards from,
// including sources that are only reachable through a converting or copying
// bridge created by toStream. Before the fix, such a bridge kept the source
// open until another chunk arrived, so the writer's Send still succeeded.

func TestMergeStreamReadersClosePropagatesToConvertedSource(t *testing.T) {
	source, writer := schema.Pipe[int](0)
	defer writer.Close()

	converted := schema.StreamReaderWithConvert(source,
		func(value int) (int, error) { return value, nil })

	merged := schema.MergeStreamReaders([]*schema.StreamReader[int]{
		converted, schema.StreamReaderFromArray([]int{}),
	})
	merged.Close()

	if closed := writer.Send(1, nil); !closed {
		t.Fatalf("expected Send to report the closed source after merged.Close(), got open")
	}
}

func TestMergeNamedStreamReadersClosePropagatesToConvertedSource(t *testing.T) {
	source, writer := schema.Pipe[int](0)
	defer writer.Close()

	converted := schema.StreamReaderWithConvert(source,
		func(value int) (int, error) { return value, nil })

	merged := schema.MergeNamedStreamReaders(map[string]*schema.StreamReader[int]{
		"converted": converted,
	})
	merged.Close()

	if closed := writer.Send(1, nil); !closed {
		t.Fatalf("expected Send to report the closed source after named merge close, got open")
	}
}

func TestNestedMergeClosePropagatesToConvertedSource(t *testing.T) {
	source, writer := schema.Pipe[int](0)
	defer writer.Close()

	converted := schema.StreamReaderWithConvert(source,
		func(value int) (int, error) { return value, nil })

	inner := schema.MergeNamedStreamReaders(map[string]*schema.StreamReader[int]{
		"converted": converted,
	})
	outer := schema.MergeStreamReaders([]*schema.StreamReader[int]{
		inner, schema.StreamReaderFromArray([]int{}),
	})
	outer.Close()

	if closed := writer.Send(1, nil); !closed {
		t.Fatalf("expected Send to report the closed source after nested merge close, got open")
	}
}

// The forwarding goroutine and the receiver-close path must close the source
// exactly once: closing a bridge whose source already reached EOF must not
// panic on a double close.
func TestMergeStreamReadersCloseAfterSourceEOFDoesNotPanic(t *testing.T) {
	source, writer := schema.Pipe[int](1)
	writer.Send(1, nil)
	writer.Close()

	converted := schema.StreamReaderWithConvert(source,
		func(value int) (int, error) { return value, nil })

	merged := schema.MergeStreamReaders([]*schema.StreamReader[int]{
		converted, schema.StreamReaderFromArray([]int{2}),
	})

	for {
		_, err := merged.Recv()
		if err != nil {
			break
		}
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("closing an exhausted merged reader panicked: %v", r)
		}
	}()
	merged.Close()
}

// Concurrent close from both sides must stay race free and must not panic.
func TestMergeStreamReadersConcurrentCloseIsSafe(t *testing.T) {
	for i := 0; i < 50; i++ {
		source, writer := schema.Pipe[int](0)
		converted := schema.StreamReaderWithConvert(source,
			func(value int) (int, error) { return value, nil })
		merged := schema.MergeStreamReaders([]*schema.StreamReader[int]{
			converted, schema.StreamReaderFromArray([]int{}),
		})

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			merged.Close()
		}()
		go func() {
			defer wg.Done()
			_ = writer.Send(1, nil)
			writer.Close()
		}()
		wg.Wait()
	}
}
