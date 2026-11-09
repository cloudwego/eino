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

package reduction

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
)

type failSecondClearWrite struct {
	filesystem.Backend
	writes int
	err    error
}

func (b *failSecondClearWrite) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	b.writes++
	if b.writes == 2 {
		return b.err
	}
	return b.Backend.Write(ctx, req)
}

func TestClearFailurePreservesStateForRetry(t *testing.T) {
	t.Run("Message", testClearFailurePreservesStateForRetry[*schema.Message])
	t.Run("AgenticMessage", testClearFailurePreservesStateForRetry[*schema.AgenticMessage])
}

func testClearFailurePreservesStateForRetry[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	fault := errors.New("second offload write failed")
	backend := &failSecondClearWrite{Backend: filesystem.NewInMemoryBackend(), err: fault}
	mw, err := NewTyped[M](ctx, &TypedConfig[M]{
		Backend:           backend,
		SkipTruncation:    true,
		MaxTokensForClear: 1,
		TokenCounter:      func(context.Context, []M, []*schema.ToolInfo) (int64, error) { return 100, nil },
		GenClearOffloadFilePath: func(_ context.Context, detail *ToolDetail) (string, error) {
			return "/clear/" + detail.ToolContext.CallID, nil
		},
	})
	require.NoError(t, err)
	state := &adk.TypedChatModelAgentState[M]{Messages: []M{
		makeUserMsgG[M]("inspect both files"),
		makeAssistantMsgWithToolCallsG[M]([]testToolCall{
			{ID: "first", Name: "lookup", Arguments: `{"path":"a"}`},
			{ID: "second", Name: "lookup", Arguments: `{"path":"b"}`},
		}),
		makeToolResultMsgG[M]("first original evidence", "first", "lookup"),
		makeToolResultMsgG[M]("second original evidence", "second", "lookup"),
		makeAssistantMsgWithToolCallsG[M]([]testToolCall{{ID: "recent", Name: "lookup", Arguments: `{"path":"c"}`}}),
		makeToolResultMsgG[M]("keep recent evidence", "recent", "lookup"),
	}}
	original := copyMessagesGeneric(state.Messages)
	_, returned, err := mw.BeforeModelRewriteState(ctx, state, nil)
	require.ErrorIs(t, err, fault)
	assert.Equal(t, original, state.Messages, "failed clearing must not partially rewrite the live state")
	assert.Same(t, state, returned)
	first, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/clear/first"})
	require.NoError(t, err)
	assert.Contains(t, first.Content, "first original evidence")

	_, _, err = mw.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	for _, call := range []string{"first", "second"} {
		stored, readErr := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/clear/" + call})
		require.NoError(t, readErr)
		assert.Contains(t, stored.Content, call+" original evidence", "retry must not overwrite evidence with a placeholder")
	}
	assert.NotEqual(t, original[:4], state.Messages[:4], "successful retry should commit clearing")
	assert.Equal(t, original[4:], state.Messages[4:], "retained turn stays unchanged")
}
