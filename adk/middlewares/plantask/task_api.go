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
	"sort"
	"strconv"
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
// NOTE: This function is NOT concurrency-safe on its own and does not consult the
// task guard. For concurrent access (and to honor the team directory constraint),
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
// NOTE: This function is NOT concurrency-safe on its own and does not consult the
// task guard. For concurrent access (and to honor the team directory constraint),
// use Middleware.DeleteTask(). In team mode it shares m.taskLock with tool calls;
// in non-team mode tools use per-turn turnLock, so the locks are not shared.
func DeleteTask(ctx context.Context, backend Backend, baseDir string, taskID string) error {
	return deleteTaskLocked(ctx, backend, baseDir, taskID)
}

// deleteTaskLocked is the core implementation of DeleteTask without locking.
// Callers must hold the appropriate lock before calling this function.
//
// Deletion is performed as a graph-level mutation: every reference to the target
// is removed from its counterparts in memory first, the modified counterparts are
// flushed in a single deterministic batch, and only then is the target file
// removed. Nothing is written until the in-memory graph is fully reconciled, so a
// validation failure never persists a partial change. On a mid-batch backend
// failure the error names the tasks it did and did not persist (and whether the
// target was deleted); because every mutation is idempotent (reference removal /
// delete), retrying the same DeleteTask reconciles any one-sided edge left behind.
func deleteTaskLocked(ctx context.Context, backend Backend, baseDir string, taskID string) error {
	if !isValidTaskID(taskID) {
		return fmt.Errorf("DeleteTask invalid task ID: %s", taskID)
	}

	// Load the whole graph once and reconcile references in memory before any
	// write, mirroring TaskUpdate's snapshot+dirty-set+batch-flush approach.
	tasks, err := listTasks(ctx, backend, baseDir, nil)
	if err != nil {
		return fmt.Errorf("DeleteTask list tasks failed, err: %w", err)
	}

	// dirty collects every counterpart whose blocks/blockedBy referenced the
	// target, keyed by ID so each is written at most once.
	dirty := make(map[string]*task)
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
			dirty[t.ID] = t
		}
	}

	// Flush the dangling-reference cleanup in one deterministic batch so a
	// mid-batch failure reports exactly which counterparts were reconciled.
	if err := persistTaskGraph(ctx, backend, baseDir, dirty); err != nil {
		return fmt.Errorf("DeleteTask %w (target Task #%s not yet deleted)", err, taskID)
	}

	// Delete the task file last: with every reference already cleared, a failure
	// here leaves a clean graph minus the still-present target, which a retry of
	// the same DeleteTask removes (the reference cleanup is then a no-op).
	if err := backend.Delete(ctx, &DeleteRequest{FilePath: taskFileJoin(baseDir, taskID)}); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // already deleted
		}
		return fmt.Errorf("DeleteTask delete task #%s failed, err: %w", taskID, err)
	}

	return nil
}

// persistTaskGraph writes every task in dirty back to the backend in one batch,
// in deterministic ID order so partial-failure reporting is stable. On a
// mid-batch failure it reports which tasks were persisted and which were not, so
// a retry of an idempotent graph mutation can reconcile any one-sided edge left
// behind. It is shared by the delete and update paths.
func persistTaskGraph(ctx context.Context, backend Backend, baseDir string, dirty map[string]*task) error {
	if len(dirty) == 0 {
		return nil
	}

	ids := make([]string, 0, len(dirty))
	for id := range dirty {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		ni, errI := strconv.ParseInt(ids[i], 10, 64)
		nj, errJ := strconv.ParseInt(ids[j], 10, 64)
		if errI == nil && errJ == nil {
			return ni < nj
		}
		return ids[i] < ids[j]
	})

	var persisted []string
	for _, id := range ids {
		if err := writeTask(ctx, backend, baseDir, dirty[id]); err != nil {
			remaining := ids[len(persisted):]
			return fmt.Errorf("persist task graph failed at Task #%s (persisted %v, not persisted %v); retry the same operation to reconcile, err: %w",
				id, persisted, remaining, err)
		}
		persisted = append(persisted, id)
	}
	return nil
}
