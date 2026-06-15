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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// LocalBackend implements Backend on the local OS filesystem.
// It is intentionally minimal (Read + GlobInfo) to match the "方案一" storage abstraction.
type LocalBackend struct{}

// NewLocalBackend returns a filesystem-backed Backend implementation.
func NewLocalBackend() *LocalBackend {
	return &LocalBackend{}
}

func (b *LocalBackend) Read(_ context.Context, req *ReadRequest) (*FileContent, error) {
	if req == nil || req.FilePath == "" {
		return nil, fmt.Errorf("read: invalid request")
	}

	raw, err := os.ReadFile(req.FilePath)
	if err != nil {
		return nil, err
	}

	content := string(raw)
	offset := req.Offset - 1
	if offset < 0 {
		offset = 0
	}
	limit := req.Limit

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

	return &FileContent{Content: strings.TrimSuffix(content[start:end], "\n")}, nil
}

func (b *LocalBackend) GlobInfo(_ context.Context, req *GlobInfoRequest) ([]FileInfo, error) {
	if req == nil || req.Pattern == "" || req.Path == "" {
		return nil, fmt.Errorf("glob: invalid request")
	}

	root := filepath.Clean(req.Path)
	var matches []FileInfo
	type item struct {
		fi FileInfo
		t  time.Time
	}
	var tmp []item

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		ok, err := doublestar.Match(req.Pattern, rel)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}

		st, err := os.Stat(path)
		if err != nil {
			return err
		}

		tmp = append(tmp, item{
			fi: FileInfo{
				Path:       path,
				IsDir:      false,
				Size:       st.Size(),
				ModifiedAt: st.ModTime().Format(time.RFC3339Nano),
			},
			t: st.ModTime(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(tmp, func(i, j int) bool { return tmp[i].t.After(tmp[j].t) })
	matches = make([]FileInfo, 0, len(tmp))
	for _, it := range tmp {
		matches = append(matches, it.fi)
	}
	return matches, nil
}

func (b *LocalBackend) Write(_ context.Context, req *WriteRequest) error {
	if req == nil || req.FilePath == "" {
		return fmt.Errorf("write: invalid request")
	}
	path := filepath.Clean(req.FilePath)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(req.Content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (b *LocalBackend) Edit(ctx context.Context, req *EditRequest) error {
	if req == nil || req.FilePath == "" {
		return fmt.Errorf("edit: invalid request")
	}
	fc, err := b.Read(ctx, &ReadRequest{FilePath: req.FilePath})
	if err != nil {
		return err
	}
	if req.OldString == "" {
		return fmt.Errorf("edit: old string must be non-empty")
	}
	if req.OldString == req.NewString {
		return fmt.Errorf("edit: new string must differ from old string")
	}

	out := fc.Content
	if req.ReplaceAll {
		out = strings.ReplaceAll(out, req.OldString, req.NewString)
	} else {
		if strings.Count(out, req.OldString) != 1 {
			return fmt.Errorf("edit: old string must appear exactly once when ReplaceAll is false")
		}
		out = strings.Replace(out, req.OldString, req.NewString, 1)
	}
	return b.Write(ctx, &WriteRequest{FilePath: req.FilePath, Content: out})
}
