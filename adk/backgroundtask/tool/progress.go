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
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

const (
	defaultRecentOutputLimit = 20
	maxRenderedProgressBytes = 16 << 10
)

// ProgressReader formats a bounded recent view of managed-tool output records.
type ProgressReader struct {
	Manager *backgroundtask.Manager
	Limit   int
}

// ReadProgress implements middleware executor-specific progress projection.
func (r *ProgressReader) ReadProgress(
	ctx context.Context,
	task *backgroundtask.Task,
) (string, error) {
	if r == nil || r.Manager == nil || task == nil {
		return "", nil
	}
	if task.Spec.ExecutorKey != ExecutorKey &&
		task.Spec.ExecutorKey != RecoverableExecutorKey {
		return "", nil
	}
	limit := r.Limit
	if limit <= 0 {
		limit = defaultRecentOutputLimit
	}
	result, err := r.Manager.ReadRecentOutput(ctx, &backgroundtask.ReadRecentOutputRequest{
		TaskID: task.Spec.ID, Limit: limit,
	})
	if err != nil {
		return "", err
	}
	if len(result.Records) == 0 {
		return "", nil
	}
	var output strings.Builder
	output.WriteString("Recent progress:")
	for _, record := range result.Records {
		line := formatProgressRecord(record)
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

func formatProgressRecord(record backgroundtask.OutputRecord) string {
	var update Update
	if err := json.Unmarshal(record.Data, &update); err != nil {
		return boundedText(record.Data, 1024)
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
	case strings.HasPrefix(update.MIMEType, "text/"),
		update.MIMEType == "application/json", update.MIMEType == "":
		content = boundedText(update.Data, 4096)
	default:
		content = fmt.Sprintf("[%s payload: %d bytes]", update.MIMEType, len(update.Data))
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
