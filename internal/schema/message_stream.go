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

import "github.com/cloudwego/eino/schema"

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
