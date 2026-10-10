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
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestCollectStreamPrefixRetainsErrorAndReaderOwnership(t *testing.T) {
	terminalErr := errors.New("stream failed")
	r, w := schema.Pipe[string](3)
	defer r.Close()
	defer w.Close()
	w.Send("prefix", nil)
	w.Send("discarded", terminalErr)
	w.Send("remaining", nil)

	chunks, err := CollectStreamPrefix(r)
	assert.Equal(t, []string{"prefix"}, chunks)
	assert.Same(t, terminalErr, err)

	// Collecting a prefix must neither consume past the error nor close the
	// reader: callers such as event serialization retain ownership.
	remaining, err := r.Recv()
	require.NoError(t, err)
	assert.Equal(t, "remaining", remaining)
}

func TestCollectStreamPrefixPreservesEOF(t *testing.T) {
	for _, terminalErr := range []error{io.EOF, fmt.Errorf("wrapped: %w", io.EOF)} {
		t.Run(terminalErr.Error(), func(t *testing.T) {
			r, w := schema.Pipe[int](2)
			defer r.Close()
			w.Send(1, nil)
			w.Send(0, terminalErr)
			w.Close()

			chunks, err := CollectStreamPrefix(r)
			assert.Equal(t, []int{1}, chunks)
			assert.Same(t, terminalErr, err)
		})
	}

	r := schema.StreamReaderFromArray([]int{})
	defer r.Close()
	chunks, err := CollectStreamPrefix(r)
	assert.Empty(t, chunks)
	assert.ErrorIs(t, err, io.EOF)
}

func TestConcatMessagesPreservesSchemaSemantics(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		for _, chunks := range [][]*schema.Message{
			nil,
			{schema.AssistantMessage("single", nil)},
			{schema.AssistantMessage("a", nil), schema.AssistantMessage("b", nil)},
			{schema.AssistantMessage("a", nil), schema.UserMessage("b")},
			{nil},
		} {
			expected, expectedErr := schema.ConcatMessages(chunks)
			actual, actualErr := ConcatMessages(chunks)
			assert.Equal(t, expected, actual)
			if expectedErr != nil {
				require.EqualError(t, actualErr, expectedErr.Error())
			} else {
				require.NoError(t, actualErr)
			}
		}
	})
	t.Run("agentic message", func(t *testing.T) {
		single := &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}
		for _, chunks := range [][]*schema.AgenticMessage{
			nil,
			{single},
			{single, single},
			{single, {Role: schema.AgenticRoleTypeUser}},
			{nil},
		} {
			expected, expectedErr := schema.ConcatAgenticMessages(chunks)
			actual, actualErr := ConcatMessages(chunks)
			assert.Equal(t, expected, actual)
			if len(chunks) == 1 {
				assert.Same(t, chunks[0], actual)
			}
			if expectedErr != nil {
				require.EqualError(t, actualErr, expectedErr.Error())
			} else {
				require.NoError(t, actualErr)
			}
		}
	})
}
