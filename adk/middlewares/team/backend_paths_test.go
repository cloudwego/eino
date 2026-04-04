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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTeamDirPath(t *testing.T) {
	result := teamDirPath("/base", "alpha")
	assert.Equal(t, filepath.Join("/base", "teams", "alpha"), result)
}

func TestInboxDirPath(t *testing.T) {
	result := inboxDirPath("/base", "alpha")
	assert.Equal(t, filepath.Join("/base", "teams", "alpha", "inboxes"), result)
}

func TestTasksDirPath(t *testing.T) {
	result := tasksDirPath("/base", "alpha")
	assert.Equal(t, filepath.Join("/base", "tasks", "alpha"), result)
}

func TestInboxFilePath(t *testing.T) {
	result := inboxFilePath("/base", "alpha", "worker")
	assert.Equal(t, filepath.Join("/base", "teams", "alpha", "inboxes", "worker.json"), result)
}

func TestEnsureDir_CreatesWhenNotExists(t *testing.T) {
	backend := newInMemoryBackend()
	ctx := context.Background()
	dir := "/tmp/test/newdir"

	err := ensureDir(ctx, backend, dir)
	assert.NoError(t, err)
	assert.True(t, backend.dirs[dir])
}

func TestEnsureDir_NoOpWhenExists(t *testing.T) {
	backend := newInMemoryBackend()
	ctx := context.Background()
	dir := "/tmp/test/existingdir"

	backend.dirs[dir] = true

	err := ensureDir(ctx, backend, dir)
	assert.NoError(t, err)
	assert.True(t, backend.dirs[dir])
}

func TestEnsureDir_ReturnsErrorWhenExistsFails(t *testing.T) {
	expectedErr := errors.New("exists failed")
	backend := newErrBackend(expectedErr)
	ctx := context.Background()

	err := ensureDir(ctx, backend, "/tmp/test/dir")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "check dir")
	assert.ErrorIs(t, err, expectedErr)
}

type existsFalseMkdirErrBackend struct {
	inMemoryBackend
	mkdirErr error
}

func (b *existsFalseMkdirErrBackend) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (b *existsFalseMkdirErrBackend) Mkdir(_ context.Context, _ string) error {
	return b.mkdirErr
}

func TestEnsureDir_ReturnsErrorWhenMkdirFails(t *testing.T) {
	mkdirErr := errors.New("mkdir failed")
	backend := &existsFalseMkdirErrBackend{
		inMemoryBackend: *newInMemoryBackend(),
		mkdirErr:        mkdirErr,
	}
	ctx := context.Background()

	err := ensureDir(ctx, backend, "/tmp/test/dir")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "create dir")
	assert.ErrorIs(t, err, mkdirErr)
}

func TestDeleteDirIfExists_DeletesWhenExists(t *testing.T) {
	backend := newInMemoryBackend()
	ctx := context.Background()
	dir := "/tmp/test/toremove"

	backend.dirs[dir] = true
	backend.files[dir+"/file.txt"] = "content"

	err := deleteDirIfExists(ctx, backend, dir)
	assert.NoError(t, err)
	assert.False(t, backend.dirs[dir])
	_, ok := backend.files[dir+"/file.txt"]
	assert.False(t, ok)
}

func TestDeleteDirIfExists_NoOpWhenNotExists(t *testing.T) {
	backend := newInMemoryBackend()
	ctx := context.Background()

	err := deleteDirIfExists(ctx, backend, "/tmp/test/nonexistent")
	assert.NoError(t, err)
}

func TestDeleteDirIfExists_ReturnsErrorWhenExistsFails(t *testing.T) {
	expectedErr := errors.New("exists check failed")
	backend := newErrBackend(expectedErr)
	ctx := context.Background()

	err := deleteDirIfExists(ctx, backend, "/tmp/test/dir")
	assert.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
}
