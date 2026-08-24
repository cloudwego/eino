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
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk/task/background"
)

const (
	defaultRecentOutputLimit = 20
	maxRenderedProgressBytes = 16 << 10
)

// ProgressReader formats a bounded recent view of managed-tool task events.
type ProgressReader struct {
	manager *background.Manager
	limit   int
}

// NewProgressReader creates a managed-tool progress reader. Limit is the number
// of newest events rendered; non-positive values use 20 and values above the
// store maximum are capped by ListTaskEvents.
func NewProgressReader(
	manager *background.Manager,
	limit int,
) (*ProgressReader, error) {
	if manager == nil {
		return nil, errors.New("task/tool: progress reader manager is required")
	}
	return &ProgressReader{manager: manager, limit: limit}, nil
}

// ReadProgress implements middleware executor-specific progress projection.
func (r *ProgressReader) ReadProgress(
	ctx context.Context,
	task *background.TaskSnapshot,
) (string, error) {
	if r == nil || task == nil {
		return "", nil
	}
	if task.Spec.ExecutorKey != ExecutorKey &&
		task.Spec.ExecutorKey != RecoverableExecutorKey {
		return "", nil
	}
	limit := r.limit
	if limit <= 0 {
		limit = defaultRecentOutputLimit
	}
	result, err := r.manager.ListTaskEvents(ctx, &background.ListTaskEventsRequest{
		TaskID: task.Spec.ID, Limit: limit, NewestFirst: true,
	})
	if err != nil {
		return "", err
	}
	if len(result.Events) == 0 {
		return "", nil
	}
	var output strings.Builder
	output.WriteString("Recent progress:")
	for i := len(result.Events) - 1; i >= 0; i-- {
		event := result.Events[i]
		line := formatProgressEvent(event)
		if line == "" {
			continue
		}
		if output.Len()+len(line)+1 > maxRenderedProgressBytes {
			output.WriteString("\n[recent progress truncated]")
			break
		}
		output.WriteByte('\n')
		output.WriteString(line)
	}
	return output.String(), nil
}

func formatProgressEvent(event *background.TaskEvent) string {
	if event == nil {
		return ""
	}
	var update Update
	if err := json.Unmarshal(event.Data, &update); err != nil {
		return boundedText(event.Data, 1024)
	}
	label := update.Kind
	if label == "" {
		label = "update"
	}
	content := ""
	switch {
	case len(update.Data) == 0:
		if len(update.Metadata) > 0 {
			data, _ := json.Marshal(update.Metadata)
			content = string(data)
		}
	default:
		content = boundedText(update.Data, 4096)
	}
	if content == "" {
		return fmt.Sprintf("[%s]", label)
	}
	return fmt.Sprintf("[%s] %s", label, content)
}

func boundedText(data []byte, limit int) string {
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end] + "...[truncated]"
}
