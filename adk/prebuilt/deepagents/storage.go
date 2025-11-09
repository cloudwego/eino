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

package deepagents

import (
	"context"
)

// StorageItem represents an item in the storage (file or directory).
type StorageItem struct {
	// Name is the name of the item.
	Name string
	// IsDir indicates whether the item is a directory.
	IsDir bool
}

// Storage is an abstract interface for storage operations.
// Concrete implementations (InMemoryStorage, FileSystemStorage, etc.) should be provided in eino-ext.
//
// The Storage interface provides file-system-like operations:
// - Read: Read content from a path
// - Write: Write content to a path (creates or overwrites)
// - List: List items in a directory path
// - Delete: Delete a path (file or directory)
// - Exists: Check if a path exists
//
// Paths use string representation similar to file paths (e.g., "/path/to/file.txt"),
// but the actual storage backend is determined by the implementation.
//
// Implementations must be thread-safe.
type Storage interface {
	// Read reads the content from the given path.
	// Returns an error if the path does not exist or is a directory.
	Read(ctx context.Context, path string) ([]byte, error)

	// Write writes content to the given path.
	// Creates the file if it doesn't exist, or overwrites if it exists.
	// Returns an error if the parent directory doesn't exist.
	Write(ctx context.Context, path string, content []byte) error

	// List lists all items in the given directory path.
	// Returns an error if the path is not a directory or doesn't exist.
	List(ctx context.Context, path string) ([]StorageItem, error)

	// Delete deletes the given path (file or directory).
	// Returns an error if the path doesn't exist.
	Delete(ctx context.Context, path string) error

	// Exists checks if the given path exists.
	Exists(ctx context.Context, path string) (bool, error)
}
