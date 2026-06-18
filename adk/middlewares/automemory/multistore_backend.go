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
	"strings"

	adkfs "github.com/cloudwego/eino/adk/middlewares/filesystem"
)

type multiStoreBackend struct {
	stores []runtimeMemoryStore
}

func newMultiStoreBackend(stores []runtimeMemoryStore) *multiStoreBackend {
	cp := append([]runtimeMemoryStore{}, stores...)
	return &multiStoreBackend{stores: cp}
}

func (b *multiStoreBackend) routeFilePath(p string) (runtimeMemoryStore, string, error) {
	if p == "" {
		return runtimeMemoryStore{}, "", fmt.Errorf("memory backend: empty path")
	}
	if filepath.IsAbs(p) {
		var selected runtimeMemoryStore
		ok := false
		for _, store := range b.stores {
			if !isPathWithinMemoryDir(store.Path, p) {
				continue
			}
			if !ok || len(store.Path) > len(selected.Path) {
				selected = store
				ok = true
			}
		}
		if !ok {
			return runtimeMemoryStore{}, "", fmt.Errorf("memory backend: path out of bounds: %s", p)
		}
		return selected, p, nil
	}

	if store, rel, ok := b.routeStoreQualifiedPath(p); ok {
		return store, rel, nil
	}
	if len(b.stores) == 1 {
		return b.stores[0], p, nil
	}
	return runtimeMemoryStore{}, "", fmt.Errorf("memory backend: relative path is ambiguous across %d memory stores; use an absolute path or prefix it with the memory store name", len(b.stores))
}

func (b *multiStoreBackend) routeDirPath(p string) (runtimeMemoryStore, string, error) {
	if p == "" {
		if len(b.stores) == 1 {
			return b.stores[0], b.stores[0].Path, nil
		}
		return runtimeMemoryStore{}, "", fmt.Errorf("memory backend: directory path is ambiguous across %d memory stores; use an absolute path or prefix it with the memory store name", len(b.stores))
	}
	return b.routeFilePath(p)
}

func (b *multiStoreBackend) routeStoreQualifiedPath(p string) (runtimeMemoryStore, string, bool) {
	clean := filepath.ToSlash(filepath.Clean(p))
	for _, store := range b.stores {
		name := filepath.ToSlash(store.displayName())
		if clean == name {
			return store, ".", true
		}
		prefix := name + "/"
		if strings.HasPrefix(clean, prefix) {
			return store, strings.TrimPrefix(clean, prefix), true
		}
	}
	return runtimeMemoryStore{}, "", false
}

func (b *multiStoreBackend) Read(ctx context.Context, req *adkfs.ReadRequest) (*adkfs.FileContent, error) {
	if req == nil {
		return nil, fmt.Errorf("read: invalid request")
	}
	store, filePath, err := b.routeFilePath(req.FilePath)
	if err != nil {
		return nil, err
	}
	n := *req
	n.FilePath = filePath
	return store.Backend.Read(ctx, &n)
}

func (b *multiStoreBackend) Write(ctx context.Context, req *adkfs.WriteRequest) error {
	if req == nil {
		return fmt.Errorf("write: invalid request")
	}
	store, filePath, err := b.routeFilePath(req.FilePath)
	if err != nil {
		return err
	}
	n := *req
	n.FilePath = filePath
	return store.Backend.Write(ctx, &n)
}

func (b *multiStoreBackend) Edit(ctx context.Context, req *adkfs.EditRequest) error {
	if req == nil {
		return fmt.Errorf("edit: invalid request")
	}
	store, filePath, err := b.routeFilePath(req.FilePath)
	if err != nil {
		return err
	}
	n := *req
	n.FilePath = filePath
	return store.Backend.Edit(ctx, &n)
}

func (b *multiStoreBackend) GlobInfo(ctx context.Context, req *adkfs.GlobInfoRequest) ([]adkfs.FileInfo, error) {
	if req == nil || req.Pattern == "" {
		return nil, fmt.Errorf("glob: invalid request")
	}
	if req.Path == "" && len(b.stores) > 1 {
		var out []adkfs.FileInfo
		for _, store := range b.stores {
			n := *req
			n.Path = store.Path
			files, err := store.Backend.GlobInfo(ctx, &n)
			if err != nil {
				return nil, err
			}
			out = append(out, files...)
		}
		return out, nil
	}
	store, path, err := b.routeDirPath(req.Path)
	if err != nil {
		return nil, err
	}
	n := *req
	n.Path = path
	return store.Backend.GlobInfo(ctx, &n)
}

func (b *multiStoreBackend) LsInfo(ctx context.Context, req *adkfs.LsInfoRequest) ([]adkfs.FileInfo, error) {
	if req == nil {
		return nil, fmt.Errorf("ls: invalid request")
	}
	store, path, err := b.routeDirPath(req.Path)
	if err != nil {
		return nil, err
	}
	n := *req
	n.Path = path
	return store.Backend.LsInfo(ctx, &n)
}

func (b *multiStoreBackend) GrepRaw(ctx context.Context, req *adkfs.GrepRequest) ([]adkfs.GrepMatch, error) {
	if req == nil {
		return nil, fmt.Errorf("grep: invalid request")
	}
	store, path, err := b.routeDirPath(req.Path)
	if err != nil {
		return nil, err
	}
	n := *req
	n.Path = path
	return store.Backend.GrepRaw(ctx, &n)
}
