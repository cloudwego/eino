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

package schema

import (
	"errors"
	"io"

	"github.com/cloudwego/eino/schema"
)

// ConcatMessageStream consumes and closes the stream, then concatenates its
// chunks. A read error other than io.EOF aborts without concatenating the prefix.
// Empty streams follow the corresponding schema concat function's semantics.
func ConcatMessageStream[M *schema.Message | *schema.AgenticMessage](stream *schema.StreamReader[M]) (M, error) {
	defer stream.Close()
	chunks, recvErr := CollectStreamPrefix(stream)
	if recvErr != io.EOF {
		var zero M
		return zero, recvErr
	}
	return ConcatMessages(chunks)
}

// ConcatMessageStreamPrefix consumes and closes the stream, concatenating all
// successfully received chunks even when reading fails. It reports read and
// concat errors separately; errors matching io.EOF mean normal completion.
// hasChunks reports whether any chunks were received, including on concat
// failure. An empty stream returns a zero message without attempting to concat.
func ConcatMessageStreamPrefix[M *schema.Message | *schema.AgenticMessage](stream *schema.StreamReader[M]) (msg M, hasChunks bool, recvErr error, concatErr error) {
	defer stream.Close()
	chunks, recvErr := CollectStreamPrefix(stream)
	if errors.Is(recvErr, io.EOF) {
		recvErr = nil
	}
	if len(chunks) == 0 {
		return msg, false, recvErr, nil
	}
	msg, concatErr = ConcatMessages(chunks)
	return msg, true, recvErr, concatErr
}

// CollectStreamPrefix reads until the first error and returns all successfully
// received chunks and the terminal error, including io.EOF. The caller decides
// how to handle that error and is responsible for closing the stream.
func CollectStreamPrefix[T any](stream *schema.StreamReader[T]) ([]T, error) {
	var chunks []T
	for {
		chunk, err := stream.Recv()
		if err != nil {
			return chunks, err
		}
		chunks = append(chunks, chunk)
	}
}

// ConcatMessages dispatches to the schema concat function for the message type,
// preserving its behavior for empty slices and single chunks.
func ConcatMessages[M *schema.Message | *schema.AgenticMessage](chunks []M) (M, error) {
	var zero M
	switch msgs := any(chunks).(type) {
	case []*schema.Message:
		msg, err := schema.ConcatMessages(msgs)
		if err != nil {
			return zero, err
		}
		return any(msg).(M), nil
	case []*schema.AgenticMessage:
		msg, err := schema.ConcatAgenticMessages(msgs)
		if err != nil {
			return zero, err
		}
		return any(msg).(M), nil
	default:
		panic("unreachable: unknown message type")
	}
}
