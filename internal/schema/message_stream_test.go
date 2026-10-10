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

func TestConcatMessageStreamErrorPolicies(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		testConcatMessageStreamErrorPolicies(t,
			[]*schema.Message{schema.AssistantMessage("a", nil), schema.AssistantMessage("b", nil)},
			[]*schema.Message{schema.AssistantMessage("a", nil), schema.UserMessage("b")},
			schema.AssistantMessage("ab", nil), &schema.Message{})
	})
	t.Run("agentic message", func(t *testing.T) {
		testConcatMessageStreamErrorPolicies(t,
			[]*schema.AgenticMessage{{Role: schema.AgenticRoleTypeAssistant}, {Role: schema.AgenticRoleTypeAssistant}},
			[]*schema.AgenticMessage{{Role: schema.AgenticRoleTypeAssistant}, {Role: schema.AgenticRoleTypeUser}},
			&schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}, &schema.AgenticMessage{})
	})
}

func testConcatMessageStreamErrorPolicies[M *schema.Message | *schema.AgenticMessage](t *testing.T, valid, invalid []M, merged, empty M) {
	readErr := errors.New("read failed")
	wrappedEOF := fmt.Errorf("wrapped: %w", io.EOF)
	cases := []struct {
		name          string
		chunks        []M
		terminalErr   error
		strictReadErr error
		prefixReadErr error
		wantConcatErr bool
	}{
		{name: "success", chunks: valid, terminalErr: io.EOF},
		{name: "empty", terminalErr: io.EOF},
		{name: "failed before first chunk", terminalErr: readErr, strictReadErr: readErr, prefixReadErr: readErr},
		{name: "failed after prefix", chunks: valid, terminalErr: readErr, strictReadErr: readErr, prefixReadErr: readErr},
		{name: "concat failure", chunks: invalid, terminalErr: io.EOF, wantConcatErr: true},
		{name: "read and concat failure", chunks: invalid, terminalErr: readErr, strictReadErr: readErr, prefixReadErr: readErr, wantConcatErr: true},
		{name: "wrapped EOF", chunks: valid, terminalErr: wrappedEOF, strictReadErr: wrappedEOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newStream := func() (*schema.StreamReader[M], *schema.StreamWriter[M]) {
				r, w := schema.Pipe[M](len(tc.chunks) + 1)
				t.Cleanup(w.Close)
				for _, chunk := range tc.chunks {
					w.Send(chunk, nil)
				}
				var zero M
				w.Send(zero, tc.terminalErr)
				return r, w
			}

			r, w := newStream()
			msg, err := ConcatMessageStream(r)
			switch {
			case tc.strictReadErr != nil:
				assert.Same(t, tc.strictReadErr, err)
				assert.Nil(t, msg)
			case tc.wantConcatErr:
				require.ErrorContains(t, err, "different roles")
				assert.Nil(t, msg)
			default:
				require.NoError(t, err)
				if len(tc.chunks) == 0 {
					assert.Equal(t, empty, msg)
				} else {
					assert.Equal(t, merged, msg)
				}
			}
			var zero M
			assert.True(t, w.Send(zero, nil), "standard concat must close the reader")

			r, w = newStream()
			msg, hasChunks, recvErr, concatErr := ConcatMessageStreamPrefix(r)
			assert.Equal(t, len(tc.chunks) > 0, hasChunks)
			if tc.prefixReadErr != nil {
				assert.Same(t, tc.prefixReadErr, recvErr)
			} else {
				assert.NoError(t, recvErr)
			}
			if tc.wantConcatErr {
				require.ErrorContains(t, concatErr, "different roles")
				assert.Nil(t, msg)
			} else {
				require.NoError(t, concatErr)
				if hasChunks {
					assert.Equal(t, merged, msg)
				} else {
					assert.Nil(t, msg)
				}
			}
			assert.True(t, w.Send(zero, nil), "prefix concat must close the reader")
		})
	}
}
