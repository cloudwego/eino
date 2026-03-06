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
	"path/filepath"
	"strings"
	"sync"
)

type inMemoryBackend struct {
	files map[string]string
	mu    sync.RWMutex
}

func newInMemoryBackend() *inMemoryBackend {
	return &inMemoryBackend{
		files: make(map[string]string),
	}
}

func (b *inMemoryBackend) LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	reqPath := strings.TrimSuffix(req.Path, "/")
	var result []FileInfo
	for path := range b.files {
		dir := filepath.Dir(path)
		if dir == reqPath {
			result = append(result, FileInfo{Path: path})
		}
	}
	return result, nil
}

func (b *inMemoryBackend) Read(ctx context.Context, req *ReadRequest) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	content, ok := b.files[req.FilePath]
	if !ok {
		return "", errors.New("file not found")
	}
	return content, nil
}

func (b *inMemoryBackend) Write(ctx context.Context, req *WriteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.files[req.FilePath] = req.Content
	return nil
}

func (b *inMemoryBackend) Delete(ctx context.Context, req *DeleteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.files, req.FilePath)
	return nil
}
