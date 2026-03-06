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
	"regexp"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
)

var validTaskIDRegex = regexp.MustCompile(`^\d+$`)

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
)

type FileInfo = filesystem.FileInfo
type LsInfoRequest = filesystem.LsInfoRequest
type ReadRequest = filesystem.ReadRequest
type WriteRequest = filesystem.WriteRequest

type DeleteRequest struct {
	FilePath string
}

// Backend defines the storage interface for task persistence.
// Implementations can use local filesystem, remote storage, or any other storage backend.
type Backend interface {
	// LsInfo lists file information in the specified directory.
	LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error)
	// Read reads the content of a file.
	Read(ctx context.Context, req *ReadRequest) (string, error)
	// Write writes content to a file, creating it if it doesn't exist.
	Write(ctx context.Context, req *WriteRequest) error
	// Delete removes a file from storage.
	Delete(ctx context.Context, req *DeleteRequest) error
}

func isValidTaskID(taskID string) bool {
	return validTaskIDRegex.MatchString(taskID)
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
