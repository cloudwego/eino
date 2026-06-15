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

package automemory

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

type memFile struct {
	content    string
	modifiedAt time.Time
}

// InMemoryBackend is a simple in-memory Backend implementation intended for tests
// and demos. Paths are treated as filesystem-like, and should be absolute.
type InMemoryBackend struct {
	mu    sync.RWMutex
	files map[string]*memFile
}

// NewInMemoryBackend returns an empty in-memory Backend implementation.
func NewInMemoryBackend() *InMemoryBackend {
	return &InMemoryBackend{
		files: make(map[string]*memFile),
	}
}

func (b *InMemoryBackend) put(path string, content string, modifiedAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.files[filepath.Clean(path)] = &memFile{content: content, modifiedAt: modifiedAt}
}

func (b *InMemoryBackend) Write(_ context.Context, req *WriteRequest) error {
	if req == nil || req.FilePath == "" {
		return fmt.Errorf("write: invalid request")
	}
	// Default to full replace.
	b.put(req.FilePath, req.Content, time.Now())
	return nil
}

func (b *InMemoryBackend) Edit(_ context.Context, req *EditRequest) error {
	if req == nil || req.FilePath == "" {
		return fmt.Errorf("edit: invalid request")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	path := filepath.Clean(req.FilePath)
	f, ok := b.files[path]
	if !ok {
		return fmt.Errorf("file not found: %s", path)
	}

	if req.OldString == "" {
		return fmt.Errorf("edit: old string must be non-empty")
	}
	if req.OldString == req.NewString {
		return fmt.Errorf("edit: new string must differ from old string")
	}

	out := f.content
	if req.ReplaceAll {
		out = strings.ReplaceAll(out, req.OldString, req.NewString)
	} else {
		if strings.Count(out, req.OldString) != 1 {
			return fmt.Errorf("edit: old string must appear exactly once when ReplaceAll is false")
		}
		out = strings.Replace(out, req.OldString, req.NewString, 1)
	}
	f.content = out
	f.modifiedAt = time.Now()
	return nil
}

func (b *InMemoryBackend) Read(_ context.Context, req *ReadRequest) (*FileContent, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if req == nil || req.FilePath == "" {
		return nil, fmt.Errorf("read: invalid request")
	}
	path := filepath.Clean(req.FilePath)
	f, ok := b.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}

	offset := req.Offset - 1
	if offset < 0 {
		offset = 0
	}
	limit := req.Limit

	content := f.content
	if offset == 0 && limit <= 0 {
		return &FileContent{Content: content}, nil
	}

	start := 0
	for i := 0; i < offset; i++ {
		idx := strings.IndexByte(content[start:], '\n')
		if idx == -1 {
			return &FileContent{Content: ""}, nil
		}
		start += idx + 1
	}

	if limit <= 0 {
		return &FileContent{Content: content[start:]}, nil
	}

	end := start
	for i := 0; i < limit; i++ {
		idx := strings.IndexByte(content[end:], '\n')
		if idx == -1 {
			return &FileContent{Content: content[start:]}, nil
		}
		end += idx + 1
	}

	// Trim trailing newline.
	return &FileContent{Content: strings.TrimSuffix(content[start:end], "\n")}, nil
}

func (b *InMemoryBackend) GlobInfo(_ context.Context, req *GlobInfoRequest) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if req == nil || req.Pattern == "" {
		return nil, fmt.Errorf("glob: invalid request")
	}
	base := filepath.Clean(req.Path)
	if base == "." {
		base = ""
	}

	type item struct {
		fi FileInfo
		t  time.Time
	}
	var out []item

	for p, f := range b.files {
		if base != "" {
			// Require p under base.
			if p != base && !strings.HasPrefix(p, base+string(filepath.Separator)) {
				continue
			}
		}

		rel := p
		if base != "" {
			rel = strings.TrimPrefix(p, base+string(filepath.Separator))
			if rel == p {
				rel = strings.TrimPrefix(p, base)
				rel = strings.TrimPrefix(rel, string(filepath.Separator))
			}
		}
		rel = filepath.ToSlash(rel)

		ok, err := doublestar.Match(req.Pattern, rel)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		out = append(out, item{
			fi: FileInfo{
				Path:       p,
				IsDir:      false,
				Size:       int64(len(f.content)),
				ModifiedAt: f.modifiedAt.Format(time.RFC3339Nano),
			},
			t: f.modifiedAt,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].t.After(out[j].t) })
	ret := make([]FileInfo, 0, len(out))
	for _, it := range out {
		ret = append(ret, it.fi)
	}
	return ret, nil
}
