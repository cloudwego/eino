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

// Package internal contains bounded filesystem adapters used by automemory
// middleware implementations.
package internal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	adkfs "github.com/cloudwego/eino/adk/filesystem"
)

type Backend interface {
	Read(ctx context.Context, req *adkfs.ReadRequest) (*adkfs.FileContent, error)
	GlobInfo(ctx context.Context, req *adkfs.GlobInfoRequest) ([]adkfs.FileInfo, error)
	Write(ctx context.Context, req *adkfs.WriteRequest) error
	Edit(ctx context.Context, req *adkfs.EditRequest) error
}

type FSBackendConfig struct {
	BaseDir           string
	AllowLs           bool
	AllowGrep         bool
	NotFoundAsContent bool
	ErrorPrefix       string
}

type FSBackend struct {
	backend           Backend
	baseClean         string
	allowLs           bool
	allowGrep         bool
	notFoundAsContent bool
	errorPrefix       string
}

// ResolveMemoryDir returns the cleaned absolute path for a memory directory.
func ResolveMemoryDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// NewFSBackend wraps a Backend with path-bounding and optional tool behaviors.
func NewFSBackend(backend Backend, cfg FSBackendConfig) (*FSBackend, error) {
	if backend == nil {
		return nil, fmt.Errorf("%s: nil backend", prefixOrDefault(cfg.ErrorPrefix))
	}
	if cfg.BaseDir == "" {
		return nil, fmt.Errorf("%s: empty base dir", prefixOrDefault(cfg.ErrorPrefix))
	}
	baseClean, err := ResolveMemoryDir(cfg.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("%s: resolve base dir: %w", prefixOrDefault(cfg.ErrorPrefix), err)
	}
	return &FSBackend{
		backend:           backend,
		baseClean:         baseClean,
		allowLs:           cfg.AllowLs,
		allowGrep:         cfg.AllowGrep,
		notFoundAsContent: cfg.NotFoundAsContent,
		errorPrefix:       prefixOrDefault(cfg.ErrorPrefix),
	}, nil
}

func prefixOrDefault(prefix string) string {
	if prefix == "" {
		return "fs backend"
	}
	return prefix
}

func isFileNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "file not found") || strings.Contains(msg, "no such file or directory")
}

func (f *FSBackend) resolveFilePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("%s: empty path", f.errorPrefix)
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(f.baseClean, p)
	}
	p = filepath.Clean(p)
	if p != f.baseClean && !strings.HasPrefix(p, f.baseClean+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: path out of bounds: %s", f.errorPrefix, p)
	}
	return p, nil
}

func (f *FSBackend) resolveDirPath(p string) (string, error) {
	if p == "" {
		return f.baseClean, nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(f.baseClean, p)
	}
	p = filepath.Clean(p)
	if p != f.baseClean && !strings.HasPrefix(p, f.baseClean+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: dir out of bounds: %s", f.errorPrefix, p)
	}
	return p, nil
}

func (f *FSBackend) Read(ctx context.Context, req *adkfs.ReadRequest) (*adkfs.FileContent, error) {
	if req == nil {
		return nil, fmt.Errorf("read: invalid request")
	}
	fp, err := f.resolveFilePath(req.FilePath)
	if err != nil {
		return nil, err
	}
	n := *req
	n.FilePath = fp
	content, err := f.backend.Read(ctx, &n)
	if err != nil {
		if f.notFoundAsContent && isFileNotFoundErr(err) {
			return &adkfs.FileContent{Content: fmt.Sprintf("File not found: %s", fp)}, nil
		}
		return nil, err
	}
	return content, nil
}

func (f *FSBackend) Write(ctx context.Context, req *adkfs.WriteRequest) error {
	if req == nil {
		return fmt.Errorf("write: invalid request")
	}
	fp, err := f.resolveFilePath(req.FilePath)
	if err != nil {
		return err
	}
	n := *req
	n.FilePath = fp
	return f.backend.Write(ctx, &n)
}

func (f *FSBackend) Edit(ctx context.Context, req *adkfs.EditRequest) error {
	if req == nil {
		return fmt.Errorf("edit: invalid request")
	}
	fp, err := f.resolveFilePath(req.FilePath)
	if err != nil {
		return err
	}
	n := *req
	n.FilePath = fp
	return f.backend.Edit(ctx, &n)
}

func (f *FSBackend) GlobInfo(ctx context.Context, req *adkfs.GlobInfoRequest) ([]adkfs.FileInfo, error) {
	if req == nil || req.Pattern == "" {
		return nil, fmt.Errorf("glob: invalid request")
	}
	pathAbs, err := f.resolveDirPath(req.Path)
	if err != nil {
		return nil, err
	}
	pattern := req.Pattern
	if filepath.IsAbs(pattern) {
		cp := filepath.Clean(pattern)
		if cp == pathAbs {
			pattern = "."
		} else if strings.HasPrefix(cp, pathAbs+string(filepath.Separator)) {
			rel, rerr := filepath.Rel(pathAbs, cp)
			if rerr != nil {
				return nil, rerr
			}
			pattern = filepath.ToSlash(rel)
		} else if strings.HasPrefix(cp, f.baseClean+string(filepath.Separator)) {
			rel, rerr := filepath.Rel(f.baseClean, cp)
			if rerr != nil {
				return nil, rerr
			}
			pattern = filepath.ToSlash(rel)
			pathAbs = f.baseClean
		} else {
			return nil, fmt.Errorf("%s: glob pattern out of bounds: %s", f.errorPrefix, cp)
		}
	} else {
		pattern = filepath.ToSlash(pattern)
	}
	n := *req
	n.Path = pathAbs
	n.Pattern = pattern
	return f.backend.GlobInfo(ctx, &n)
}

func (f *FSBackend) LsInfo(ctx context.Context, req *adkfs.LsInfoRequest) ([]adkfs.FileInfo, error) {
	if !f.allowLs {
		return nil, fmt.Errorf("ls: disabled")
	}
	if req == nil {
		return nil, fmt.Errorf("ls: invalid request")
	}
	base, err := f.resolveDirPath(req.Path)
	if err != nil {
		return nil, err
	}
	return f.GlobInfo(ctx, &adkfs.GlobInfoRequest{Path: base, Pattern: "*"})
}

func (f *FSBackend) GrepRaw(context.Context, *adkfs.GrepRequest) ([]adkfs.GrepMatch, error) {
	if !f.allowGrep {
		return nil, fmt.Errorf("grep: disabled")
	}
	return nil, fmt.Errorf("grep: not implemented")
}
