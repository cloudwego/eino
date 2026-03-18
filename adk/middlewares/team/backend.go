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
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

// Backend extends plantask.Backend with additional methods needed by team operations.
type Backend interface {
	plantask.Backend

	// Exists checks if a file or directory at the given path exists.
	Exists(ctx context.Context, path string) (bool, error)
	// Mkdir creates a directory at the given path.
	Mkdir(ctx context.Context, path string) error
}

// FileInfo reuses the plantask type alias.
type FileInfo = plantask.FileInfo

// LsInfoRequest reuses the plantask type alias.
type LsInfoRequest = plantask.LsInfoRequest

// ReadRequest reuses the plantask type alias.
type ReadRequest = plantask.ReadRequest

// WriteRequest reuses the plantask type alias.
type WriteRequest = plantask.WriteRequest

// DeleteRequest reuses the plantask type alias.
type DeleteRequest = plantask.DeleteRequest

// teamDirPath returns the team directory path under baseDir.
// Path: {baseDir}/teams/{teamName}/
func teamDirPath(baseDir, teamName string) string {
	return filepath.Join(baseDir, "teams", teamName)
}

// inboxDirPath returns the inbox directory path for an agent under baseDir.
// Path: {baseDir}/teams/{teamName}/inboxes/
func inboxDirPath(baseDir, teamName string) string {
	return filepath.Join(teamDirPath(baseDir, teamName), "inboxes")
}

// tasksDirPath returns the shared tasks directory path under baseDir.
// Path: {baseDir}/tasks/{teamName}/
func tasksDirPath(baseDir, teamName string) string {
	return filepath.Join(baseDir, "tasks", teamName)
}

// fileExists checks if a file exists via the backend.
func fileExists(ctx context.Context, backend Backend, path string) (bool, error) {
	return backend.Exists(ctx, path)
}

// listJSONFiles lists .json files in a directory via the backend.
func listJSONFiles(ctx context.Context, backend Backend, dir string) ([]string, error) {
	files, err := backend.LsInfo(ctx, &LsInfoRequest{Path: dir})
	if err != nil {
		// directory may not exist
		return nil, nil
	}
	var names []string
	for _, f := range files {
		name := filepath.Base(f.Path)
		if strings.HasSuffix(name, ".json") {
			names = append(names, name)
		}
	}
	return names, nil
}

// ensureDir creates a directory at the given path.
func ensureDir(ctx context.Context, backend Backend, dir string) error {
	err := backend.Mkdir(ctx, dir)
	if err != nil {
		return fmt.Errorf("create dir %q: %w", dir, err)
	}
	return nil
}

// deleteDirIfExists deletes a directory and all its contents if it exists.
func deleteDirIfExists(ctx context.Context, backend Backend, path string) error {
	exists, err := backend.Exists(ctx, path)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return backend.Delete(ctx, &DeleteRequest{FilePath: path})
}

func resolveListedPath(parentDir, listedPath string) string {
	if listedPath == "" || filepath.IsAbs(listedPath) {
		return listedPath
	}
	return filepath.Join(parentDir, listedPath)
}

// errTeamNotFound is returned when no active team exists.
var errTeamNotFound = errors.New("no active team, create a team first with TeamCreate")
