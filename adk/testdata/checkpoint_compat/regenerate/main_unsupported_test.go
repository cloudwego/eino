//go:build !linux && !darwin
// +build !linux,!darwin

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

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunFailsClosedBeforeGitOrFilesystemMutation(t *testing.T) {
	parent := t.TempDir()
	sentinel := filepath.Join(parent, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(parent, "output")
	gitCalls := 0
	runGit := func(context.Context, string, ...string) (string, error) {
		gitCalls++
		return "", errors.New("git must not be called")
	}
	var stdout bytes.Buffer

	err := runWithGit(context.Background(), outputDir, runGit, &stdout)
	want := "atomic output publication is unsupported on " + runtime.GOOS
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
	if gitCalls != 0 {
		t.Fatalf("git calls = %d, want 0", gitCalls)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	data, readErr := os.ReadFile(sentinel)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "unchanged" {
		t.Fatalf("sentinel = %q, want unchanged", data)
	}
	if _, statErr := os.Lstat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output was mutated: %v", statErr)
	}
	entries, readDirErr := os.ReadDir(parent)
	if readDirErr != nil {
		t.Fatal(readDirErr)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("parent entries = %v, want only sentinel", entries)
	}
}

func TestPublishOutputFailsClosedBeforeFilesystemMutation(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "output")
	err := publishOutput("", outputDir,
		[]generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}},
		regenerationHooks{})
	want := "atomic output publication is unsupported on " + runtime.GOOS
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want unsupported-platform error", err)
	}
	if _, statErr := os.Lstat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output was mutated: %v", statErr)
	}
}
