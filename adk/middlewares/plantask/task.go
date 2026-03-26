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
	"regexp"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
)

var validTaskIDRegex = regexp.MustCompile(`^\d+$`)

var validTaskStatuses = map[string]struct{}{
	taskStatusPending:    {},
	taskStatusInProgress: {},
	taskStatusCompleted:  {},
	taskStatusDeleted:    {},
}

const highWatermarkFileName = ".highwatermark"

type task struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	Status      string         `json:"status"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type taskOut struct {
	Result string `json:"result"`
}

const (
	taskStatusPending    = "pending"
	taskStatusInProgress = "in_progress"
	taskStatusCompleted  = "completed"
	taskStatusDeleted    = "deleted"

	// MetadataKeyInternal marks a task as system-internal (e.g., teammate shadow tasks).
	// Internal tasks are filtered out from TaskList.
	MetadataKeyInternal = "_internal"
)

type FileInfo = filesystem.FileInfo
type LsInfoRequest = filesystem.LsInfoRequest
type ReadRequest = filesystem.ReadRequest
type WriteRequest = filesystem.WriteRequest

// DeleteRequest describes a file or directory deletion.
type DeleteRequest struct {
	FilePath string
}

// Backend defines the storage interface for task persistence.
// Implementations can use local filesystem, remote storage, or any other storage backend.
type Backend interface {
	// LsInfo lists file information in the specified directory.
	LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error)
	// Read reads the content of a file.
	Read(ctx context.Context, req *ReadRequest) (*filesystem.FileContent, error)
	// Write writes content to a file, creating it if it doesn't exist.
	Write(ctx context.Context, req *WriteRequest) error
	// Delete removes a file or directory at the given path from storage.
	// If the path is a directory, it must be deleted along with all its contents,
	// regardless of whether the directory is empty.
	Delete(ctx context.Context, req *DeleteRequest) error
}

func isValidTaskID(taskID string) bool {
	return validTaskIDRegex.MatchString(taskID)
}

func isValidTaskStatus(status string) bool {
	_, ok := validTaskStatuses[status]
	return ok
}

// isInternalTask returns true if the task is marked as system-internal.
func isInternalTask(t *task) bool {
	if t.Metadata == nil {
		return false
	}
	v, ok := t.Metadata[MetadataKeyInternal].(bool)
	return ok && v
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func appendUnique(slice []string, items ...string) []string {
	seen := make(map[string]struct{}, len(slice))
	for _, s := range slice {
		seen[s] = struct{}{}
	}
	for _, item := range items {
		if _, exists := seen[item]; !exists {
			slice = append(slice, item)
			seen[item] = struct{}{}
		}
	}
	return slice
}

func hasCyclicDependency(taskMap map[string]*task, blockerID, blockedID string) bool {
	if blockerID == blockedID {
		return true
	}

	visited := make(map[string]bool)
	return canReach(taskMap, blockedID, blockerID, visited)
}

func canReach(taskMap map[string]*task, fromID, toID string, visited map[string]bool) bool {
	if fromID == toID {
		return true
	}
	if visited[fromID] {
		return false
	}
	visited[fromID] = true

	fromTask, exists := taskMap[fromID]
	if !exists {
		return false
	}

	for _, blockedID := range fromTask.Blocks {
		if canReach(taskMap, blockedID, toID, visited) {
			return true
		}
	}

	return false
}

// taskFileName returns the JSON filename for a task ID, e.g. "42.json".
func taskFileName(taskID string) string {
	return taskID + ".json"
}

// taskFileJoin returns the full path to a task's JSON file.
func taskFileJoin(baseDir, taskID string) string {
	return filepath.Join(baseDir, taskFileName(taskID))
}

// readTask reads and unmarshals a single task from the backend.
func readTask(ctx context.Context, backend Backend, baseDir, taskID string) (*task, error) {
	content, err := backend.Read(ctx, &ReadRequest{
		FilePath: taskFileJoin(baseDir, taskID),
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read task #%s failed: %w", taskID, err)
	}

	t := &task{}
	if err := sonic.UnmarshalString(content.Content, t); err != nil {
		return nil, fmt.Errorf("parse task #%s failed: %w", taskID, err)
	}
	return t, nil
}

// writeTask marshals and writes a task to the backend.
func writeTask(ctx context.Context, backend Backend, baseDir string, t *task) error {
	data, err := sonic.MarshalString(t)
	if err != nil {
		return fmt.Errorf("marshal task #%s failed: %w", t.ID, err)
	}
	if err := backend.Write(ctx, &WriteRequest{
		FilePath: taskFileJoin(baseDir, t.ID),
		Content:  data,
	}); err != nil {
		return fmt.Errorf("write task #%s failed: %w", t.ID, err)
	}
	return nil
}

// marshalTaskResponse marshals a taskOut result string into the standard tool response JSON.
func marshalTaskResponse(result string) (string, error) {
	return sonic.MarshalString(&taskOut{Result: result})
}
