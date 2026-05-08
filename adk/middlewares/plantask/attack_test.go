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
	"path/filepath"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
)

// TestDeleteTaskInvalidID_PathTraversal verifies that deleting with a
// path-traversal or non-numeric ID is rejected by validation.
func TestDeleteTaskInvalidID_PathTraversal(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/attack_tasks"

	testCases := []struct {
		name string
		id   string
	}{
		{"path traversal", "../etc/passwd"},
		{"special chars", "1; rm -rf /"},
		{"negative number", "-1"},
		{"float", "1.5"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := DeleteTask(ctx, backend, baseDir, tc.id)
			assert.Error(t, err, "DeleteTask should reject invalid ID: %q", tc.id)
			assert.Contains(t, err.Error(), "invalid task ID")
		})
	}
}

// TestStaleHighwatermark verifies that when a task file exists with a
// higher ID than the highwatermark, the system correctly uses the max file ID
// to avoid ID collisions.
func TestStaleHighwatermark(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/attack_tasks"

	// Write a task file as "5.json" directly
	taskData := &task{
		ID:        "5",
		Subject:   "Existing task",
		Status:    taskStatusPending,
		Blocks:    []string{},
		BlockedBy: []string{},
	}
	data, err := sonic.MarshalString(taskData)
	assert.NoError(t, err)
	err = backend.Write(ctx, &WriteRequest{
		FilePath: filepath.Join(baseDir, "5.json"),
		Content:  data,
	})
	assert.NoError(t, err)

	// Set highwatermark to "3" (stale — lower than max file ID)
	err = backend.Write(ctx, &WriteRequest{
		FilePath: filepath.Join(baseDir, highWatermarkFileName),
		Content:  "3",
	})
	assert.NoError(t, err)

	// Create a new task — should get ID "6" (max(3,5) + 1)
	taskID, err := CreateTask(ctx, backend, baseDir, &TaskInput{
		Subject:     "New task after stale HW",
		Description: "Should get ID 6",
	})
	assert.NoError(t, err)
	assert.Equal(t, "6", taskID, "New task should get ID 6 = max(highwatermark=3, maxFileID=5) + 1")

	// Verify the highwatermark was updated
	hwContent, err := backend.Read(ctx, &ReadRequest{
		FilePath: filepath.Join(baseDir, highWatermarkFileName),
	})
	assert.NoError(t, err)
	assert.Equal(t, "6", hwContent.Content)
}

// TestHighwatermarkCorrupted verifies that when the highwatermark file
// contains non-numeric garbage, the system falls back to using the max existing
// file ID.
func TestHighwatermarkCorrupted(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/attack_tasks"

	// Write a valid task file as "2.json"
	taskData := &task{
		ID:        "2",
		Subject:   "Existing task",
		Status:    taskStatusPending,
		Blocks:    []string{},
		BlockedBy: []string{},
	}
	data, err := sonic.MarshalString(taskData)
	assert.NoError(t, err)
	err = backend.Write(ctx, &WriteRequest{
		FilePath: filepath.Join(baseDir, "2.json"),
		Content:  data,
	})
	assert.NoError(t, err)

	// Corrupt the highwatermark file
	err = backend.Write(ctx, &WriteRequest{
		FilePath: filepath.Join(baseDir, highWatermarkFileName),
		Content:  "not_a_number_!!!",
	})
	assert.NoError(t, err)

	// Create a new task — should fall back to maxFileID (2) + 1 = 3
	taskID, err := CreateTask(ctx, backend, baseDir, &TaskInput{
		Subject:     "Task after corrupted HW",
		Description: "Should get ID 3",
	})
	assert.NoError(t, err)
	assert.Equal(t, "3", taskID, "Should fall back to maxFileID=2 + 1 = 3 when highwatermark is corrupted")
}
