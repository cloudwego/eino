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

	"github.com/cloudwego/eino/adk/task/background"
)

func TestNewProgressReaderRequiresManager(t *testing.T) {
	_, err := NewProgressReader(nil, 0)
	require.EqualError(t, err, "task/tool: progress reader manager is required")
}

func TestProgressReaderFormatsBoundedRecentUpdates(t *testing.T) {
	store := background.NewInMemoryStore(nil)
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{Tasks: store})
	created, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: background.Spec{
			ID: "progress", ExecutorKey: RecoverableExecutorKey, Kind: "background_tool",
		},
		LeaseExpiryPolicy: background.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	created, err = store.Start(context.Background(), &background.StartTaskRequest{
		TaskID: created.Spec.ID, ExpectedVersion: created.Version,
	})
	require.NoError(t, err)
	for index, text := range []string{"one", "two", "three"} {
		data, marshalErr := json.Marshal(&Update{
			EventID: "event-" + text, Kind: "stdout", Data: []byte(text),
		})
		require.NoError(t, marshalErr)
		_, err = store.AppendTaskEvent(context.Background(), &background.AppendTaskEventRequest{
			TaskID: created.Spec.ID, Attempt: created.Attempt,
			EventID: "event-" + text, Data: data,
		})
		require.NoError(t, err, index)
	}
	reader, err := NewProgressReader(manager, 2)
	require.NoError(t, err)
	progress, err := reader.ReadProgress(context.Background(), created)
	require.NoError(t, err)
	require.NotContains(t, progress, "one")
	require.Contains(t, progress, "[stdout] two")
	require.Contains(t, progress, "[stdout] three")
	require.LessOrEqual(t, len(progress), maxRenderedProgressBytes)
}

func TestProgressReaderFallsBackForUnknownCompatibleRecord(t *testing.T) {
	store := background.NewInMemoryStore(nil)
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{Tasks: store})
	task, err := store.Create(context.Background(), &background.CreateTaskRequest{
		Spec: background.Spec{
			ID: "raw", ExecutorKey: ExecutorKey, Kind: "background_tool",
		},
		LeaseExpiryPolicy: background.LeaseExpiryFail,
	})
	require.NoError(t, err)
	task, err = store.Start(context.Background(), &background.StartTaskRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.Version,
	})
	require.NoError(t, err)
	_, err = store.AppendTaskEvent(context.Background(), &background.AppendTaskEventRequest{
		TaskID: task.Spec.ID, Attempt: task.Attempt,
		EventID: "raw", Data: []byte("legacy raw text"),
	})
	require.NoError(t, err)
	reader, err := NewProgressReader(manager, 0)
	require.NoError(t, err)
	progress, err := reader.ReadProgress(context.Background(), task)
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
