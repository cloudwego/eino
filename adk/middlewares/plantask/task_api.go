/*
 * Copyright 2025 CloudWeGo Authors
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

package plantask

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TaskInput is the input for creating a task programmatically.
type TaskInput struct {
	Subject     string
	Description string
	Status      string // defaults to "pending" if empty
	ActiveForm  string
	Metadata    map[string]any
}

// CreateTask creates a task programmatically (not via tool call).
// Returns the new task ID.
//
// NOTE: This function is NOT concurrency-safe on its own. For concurrent access,
// use Middleware.CreateTask(). In team mode it shares m.taskLock with tool calls;
// in non-team mode tools use per-turn turnLock, so the locks are not shared.
func CreateTask(ctx context.Context, backend Backend, baseDir string, input *TaskInput) (string, error) {
	if input == nil {
		return "", fmt.Errorf("CreateTask input is nil")
	}
	return createTaskLocked(ctx, backend, baseDir, input)
}

// createTaskLocked is the core implementation of CreateTask without locking.
// Callers must hold the appropriate lock before calling this function.
func createTaskLocked(ctx context.Context, backend Backend, baseDir string, input *TaskInput) (string, error) {
	files, err := backend.LsInfo(ctx, &LsInfoRequest{
		Path: baseDir,
	})
	if err != nil {
		return "", fmt.Errorf("CreateTask list files in %s failed, err: %w", baseDir, err)
	}

	highwatermark := int64(0)
	maxFileID := int64(0)
	for _, file := range files {
		fileName := filepath.Base(file.Path)
		if fileName == highWatermarkFileName {
			content, readErr := backend.Read(ctx, &ReadRequest{
				FilePath: file.Path,
			})
			if readErr != nil {
				return "", fmt.Errorf("CreateTask read highwatermark file %s failed, err: %w", file.Path, readErr)
			}
			if content != nil && content.Content != "" {
				var val int64
				if _, scanErr := fmt.Sscanf(content.Content, "%d", &val); scanErr == nil {
					highwatermark = val
				}
			}
			continue
		}
		// Track max existing task file ID to handle stale highwatermark.
		if idStr := strings.TrimSuffix(fileName, ".json"); idStr != fileName {
			var fileID int64
			if _, scanErr := fmt.Sscanf(idStr, "%d", &fileID); scanErr == nil && fileID > maxFileID {
				maxFileID = fileID
			}
		}
	}

	// Use the greater of highwatermark and max existing file ID to avoid collisions
	// when the highwatermark is stale (e.g., previous highwatermark write failed).
	taskID := highwatermark
	if maxFileID > taskID {
		taskID = maxFileID
	}
	taskID++
	taskIDStr := fmt.Sprintf("%d", taskID)

	status := input.Status
	if status == "" {
		status = taskStatusPending
	} else if !isValidTaskStatus(status) {
		return "", fmt.Errorf("CreateTask invalid task status: %s", status)
	}

	newTask := &task{
		ID:          taskIDStr,
		Subject:     input.Subject,
		Description: input.Description,
		Status:      status,
		Blocks:      []string{},
		BlockedBy:   []string{},
		ActiveForm:  input.ActiveForm,
		Metadata:    input.Metadata,
	}

	// Write task file first, then update highwatermark.
	// This ordering ensures that if the task write fails, the highwatermark
	// is not advanced, avoiding ID gaps. If the highwatermark write fails
	// after a successful task write, the next createTaskLocked call will
	// detect the existing file via maxFileID and increment past it.
	if err := writeTask(ctx, backend, baseDir, newTask); err != nil {
		return "", fmt.Errorf("CreateTask %w", err)
	}

	highwatermarkPath := filepath.Join(baseDir, highWatermarkFileName)
	if err := backend.Write(ctx, &WriteRequest{
		FilePath: highwatermarkPath,
		Content:  taskIDStr,
	}); err != nil {
		return "", fmt.Errorf("CreateTask update highwatermark failed, err: %w", err)
	}

	return taskIDStr, nil
}

// DeleteTask deletes a task and cleans up dangling dependency references.
//
// NOTE: This function is NOT concurrency-safe on its own. For concurrent access,
// use Middleware.DeleteTask(). In team mode it shares m.taskLock with tool calls;
// in non-team mode tools use per-turn turnLock, so the locks are not shared.
func DeleteTask(ctx context.Context, backend Backend, baseDir string, taskID string) error {
	return deleteTaskLocked(ctx, backend, baseDir, taskID)
}

// deleteTaskLocked is the core implementation of DeleteTask without locking.
// Callers must hold the appropriate lock before calling this function.
func deleteTaskLocked(ctx context.Context, backend Backend, baseDir string, taskID string) error {
	if !isValidTaskID(taskID) {
		return fmt.Errorf("DeleteTask invalid task ID: %s", taskID)
	}

	// Remove dangling references from other tasks.
	tasks, err := listTasks(ctx, backend, baseDir)
	if err != nil {
		return fmt.Errorf("DeleteTask list tasks failed, err: %w", err)
	}

	for _, t := range tasks {
		if t.ID == taskID {
			continue
		}

		modified := false
		newBlocks := make([]string, 0, len(t.Blocks))
		for _, id := range t.Blocks {
			if id != taskID {
				newBlocks = append(newBlocks, id)
			} else {
				modified = true
			}
		}

		newBlockedBy := make([]string, 0, len(t.BlockedBy))
		for _, id := range t.BlockedBy {
			if id != taskID {
				newBlockedBy = append(newBlockedBy, id)
			} else {
				modified = true
			}
		}

		if modified {
			t.Blocks = newBlocks
			t.BlockedBy = newBlockedBy

			if writeErr := writeTask(ctx, backend, baseDir, t); writeErr != nil {
				return fmt.Errorf("DeleteTask %w", writeErr)
			}
		}
	}

	// Delete the task file.
	if err := backend.Delete(ctx, &DeleteRequest{FilePath: taskFileJoin(baseDir, taskID)}); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // already deleted
		}
		return fmt.Errorf("DeleteTask delete task #%s failed, err: %w", taskID, err)
	}

	return nil
}
