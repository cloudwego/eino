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

package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

func TestProgressReaderFormatsBoundedRecentUpdates(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(nil)
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Store: store})
	created, err := store.CreateAndStart(context.Background(), &backgroundtask.CreateTaskRequest{
		Spec: backgroundtask.Spec{
			ID: "progress", ExecutorKey: RecoverableExecutorKey, Kind: "background_tool",
		},
		LeaseExpiryPolicy: backgroundtask.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	for index, text := range []string{"one", "two", "three"} {
		data, marshalErr := json.Marshal(&Update{
			SourceID: "event-" + text, Kind: "stdout", Data: []byte(text),
		})
		require.NoError(t, marshalErr)
		_, err = store.AppendOutputOnce(context.Background(), &backgroundtask.AppendOutputOnceRequest{
			TaskID: created.Spec.ID, Attempt: created.Attempt,
			SourceID: "event-" + text, Data: data,
		})
		require.NoError(t, err, index)
	}
	reader := &ProgressReader{Manager: manager, Limit: 2}
	progress, err := reader.ReadProgress(context.Background(), created)
	require.NoError(t, err)
	require.NotContains(t, progress, "one")
	require.Contains(t, progress, "[stdout] two")
	require.Contains(t, progress, "[stdout] three")
	require.LessOrEqual(t, len(progress), maxRenderedProgressBytes)
}

func TestProgressReaderFallsBackForUnknownCompatibleRecord(t *testing.T) {
	store := backgroundtask.NewInMemoryStore(nil)
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Store: store})
	task, err := store.CreateAndStart(context.Background(), &backgroundtask.CreateTaskRequest{
		Spec: backgroundtask.Spec{
			ID: "raw", ExecutorKey: ExecutorKey, Kind: "background_tool",
		},
		LeaseExpiryPolicy: backgroundtask.LeaseExpiryFail,
	})
	require.NoError(t, err)
	_, err = store.AppendOutput(context.Background(), &backgroundtask.AppendOutputRequest{
		TaskID: task.Spec.ID, Attempt: task.Attempt, Data: []byte("legacy raw text"),
	})
	require.NoError(t, err)
	progress, err := (&ProgressReader{Manager: manager}).ReadProgress(
		context.Background(), task,
	)
	require.NoError(t, err)
	require.Contains(t, progress, "legacy raw text")
}

func TestBoundedTextPreservesValidUTF8(t *testing.T) {
	text := strings.Repeat("a", 4095) + "界"
	rendered := boundedText([]byte(text), 4096)
	require.True(t, utf8.ValidString(rendered))
	require.NotContains(t, rendered, "\uFFFD")
	require.Contains(t, rendered, "[truncated]")
}
