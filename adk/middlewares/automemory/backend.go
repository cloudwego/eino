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
	"github.com/cloudwego/eino/adk/filesystem"
	ainternal "github.com/cloudwego/eino/adk/middlewares/automemory/internal"
)

// Backend is the only filesystem storage abstraction users need to implement
// for automemory and dream.
//
// It intentionally exposes only the capabilities required by memory loading and
// consolidation: Read, GlobInfo, Write, and Edit.
//
// LocalBackend and InMemoryBackend both implement this interface.
type Backend = ainternal.Backend

type ReadRequest = filesystem.ReadRequest
type FileContent = filesystem.FileContent
type GlobInfoRequest = filesystem.GlobInfoRequest
type FileInfo = filesystem.FileInfo
type WriteRequest = filesystem.WriteRequest
type EditRequest = filesystem.EditRequest

func newFSBackend(backend Backend, baseDir string) (*ainternal.FSBackend, error) {
	return ainternal.NewFSBackend(backend, ainternal.FSBackendConfig{
		BaseDir:           baseDir,
		NotFoundAsContent: true,
		ErrorPrefix:       "fs backend",
	})
}
