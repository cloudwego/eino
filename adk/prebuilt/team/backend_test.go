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

package team

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	fspkg "github.com/cloudwego/eino/adk/filesystem"
)

type inMemoryBackend struct {
	files map[string]string
	dirs  map[string]bool
	mu    sync.RWMutex
}

func newInMemoryBackend() *inMemoryBackend {
	return &inMemoryBackend{
		files: make(map[string]string),
		dirs:  make(map[string]bool),
	}
}

func (b *inMemoryBackend) LsInfo(_ context.Context, req *LsInfoRequest) ([]FileInfo, error) {
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

func (b *inMemoryBackend) Read(_ context.Context, req *ReadRequest) (*fspkg.FileContent, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	content, ok := b.files[req.FilePath]
	if !ok {
		return nil, errors.New("file not found")
	}
	return &fspkg.FileContent{Content: content}, nil
}

func (b *inMemoryBackend) Write(_ context.Context, req *WriteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.files[req.FilePath] = req.Content
	return nil
}

func (b *inMemoryBackend) Delete(_ context.Context, req *DeleteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	prefix := req.FilePath + "/"
	for k := range b.files {
		if k == req.FilePath || strings.HasPrefix(k, prefix) {
			delete(b.files, k)
		}
	}
	for k := range b.dirs {
		if k == req.FilePath || strings.HasPrefix(k, prefix) {
			delete(b.dirs, k)
		}
	}
	return nil
}

func (b *inMemoryBackend) Exists(_ context.Context, path string) (bool, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if _, ok := b.files[path]; ok {
		return true, nil
	}
	if b.dirs[path] {
		return true, nil
	}
	return false, nil
}

func (b *inMemoryBackend) Mkdir(_ context.Context, path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.dirs[path] = true
	return nil
}

type errBackend struct {
	err error
}

func newErrBackend(err error) *errBackend {
	return &errBackend{err: err}
}

func (b *errBackend) LsInfo(_ context.Context, _ *LsInfoRequest) ([]FileInfo, error) {
	return nil, b.err
}

func (b *errBackend) Read(_ context.Context, _ *ReadRequest) (*fspkg.FileContent, error) {
	return nil, b.err
}

func (b *errBackend) Write(_ context.Context, _ *WriteRequest) error {
	return b.err
}

func (b *errBackend) Delete(_ context.Context, _ *DeleteRequest) error {
	return b.err
}

func (b *errBackend) Exists(_ context.Context, _ string) (bool, error) {
	return false, b.err
}

func (b *errBackend) Mkdir(_ context.Context, _ string) error {
	return b.err
}

// nilContentBackend embeds an inMemoryBackend but returns (nil, nil) from Read,
// which the Backend.Read contract permits for a missing file. It exists to verify
// that readers (e.g. configStore.readConfig) tolerate a nil *FileContent without
// dereferencing it.
type nilContentBackend struct {
	*inMemoryBackend
}

func (b *nilContentBackend) Read(_ context.Context, _ *ReadRequest) (*fspkg.FileContent, error) {
	return nil, nil
}

// failingWriteBackend wraps an inMemoryBackend and fails Write for any path
// whose file name ends with failPathSuffix. It is used to simulate a partial
// broadcast where some recipients' inboxes cannot be written.
type failingWriteBackend struct {
	*inMemoryBackend
	failPathSuffix string
}

func (b *failingWriteBackend) Write(ctx context.Context, req *WriteRequest) error {
	if b.failPathSuffix != "" && strings.HasSuffix(req.FilePath, b.failPathSuffix) {
		return errors.New("write failed for " + req.FilePath)
	}
	return b.inMemoryBackend.Write(ctx, req)
}

// failReadAfterBackend wraps an inMemoryBackend and starts failing Read for a
// matching path after a given number of successful reads. It is used to inject a
// read error into the blocking poll loop after the initial read has succeeded.
type failReadAfterBackend struct {
	*inMemoryBackend
	failSuffix string
	failAfter  int

	mu        sync.Mutex
	readCount int
}

func (b *failReadAfterBackend) Read(ctx context.Context, req *ReadRequest) (*fspkg.FileContent, error) {
	if b.failSuffix != "" && strings.HasSuffix(req.FilePath, b.failSuffix) {
		b.mu.Lock()
		b.readCount++
		fail := b.readCount > b.failAfter
		b.mu.Unlock()
		if fail {
			return nil, errors.New("read failed for " + req.FilePath)
		}
	}
	return b.inMemoryBackend.Read(ctx, req)
}
