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

// Package main reproduces the frozen checkpoint compatibility fixtures.
package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	producerCommit          = "60e1d9929cb65c8c4814b66fba2854e29b730114"
	producerVersion         = "v0.9.18"
	generatorCommit         = "3e3e994e7b10955c336ae38a610ddbff5e371521"
	checkpointFormatVersion = 0
	fixtureRelativeDir      = "adk/testdata/checkpoint_compat/main_60e1d992"
	stagingDirectoryPrefix  = ".eino-checkpoint-stage-"
)

type manifest struct {
	ProducerCommit          string    `json:"producer_commit"`
	ProducerVersion         string    `json:"producer_version"`
	GeneratorCommit         string    `json:"generator_commit"`
	CheckpointFormatVersion int       `json:"checkpoint_format_version"`
	Fixtures                []fixture `json:"fixtures"`
}

type historicalManifest struct {
	ProducerCommit string    `json:"producer_commit"`
	Fixtures       []fixture `json:"fixtures"`
}

type fixture struct {
	Name               string   `json:"name"`
	File               string   `json:"file"`
	SHA256             string   `json:"sha256"`
	Depth              int      `json:"depth"`
	ParallelChildren   int      `json:"parallel_children,omitempty"`
	Streaming          bool     `json:"streaming,omitempty"`
	Cancel             bool     `json:"cancel,omitempty"`
	ImplicitResume     bool     `json:"implicit_resume,omitempty"`
	ResumeTargetCount  int      `json:"resume_target_count,omitempty"`
	ExpectedInterrupts int      `json:"expected_interrupts,omitempty"`
	PayloadField       string   `json:"payload_field"`
	PayloadSize        int      `json:"payload_size"`
	InterruptIDs       []string `json:"interrupt_ids"`
	InterruptAddresses []string `json:"interrupt_addresses"`
}

func main() {
	outputDir := flag.String("output", "", "new directory for regenerated fixtures")
	flag.Parse()
	if err := run(context.Background(), *outputDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type gitCommand func(context.Context, string, ...string) (string, error)

type directoryIdentity struct {
	device uint64
	inode  uint64
}

type regenerationHooks struct {
	beforeOutputReserved             func()
	afterOutputReserved              func()
	inspectOutputStageEntry          func(int, string) (directoryIdentity, error)
	openOutputStage                  func(int, string) (*os.File, error)
	inspectOutputStageDescriptor     func(*os.File) (directoryIdentity, error)
	afterOutputStageOpened           func(int, string) error
	inspectOutputStage               func(*os.File) (os.FileInfo, error)
	makeOutputCandidate              func(int, string) error
	inspectOutputCandidateEntry      func(int, string) (directoryIdentity, error)
	openOutputCandidate              func(int, string) (*os.File, error)
	inspectOutputCandidateDescriptor func(*os.File) (directoryIdentity, error)
	afterOutputCandidateOpened       func(int, string) error
	inspectOutputCandidate           func(*os.File) (os.FileInfo, error)
	writeOutputFile                  func(string, io.Writer, []byte) error
	closeOutputFile                  func(string, *os.File) error
	beforeOutputPublished            func()
	publishOutput                    func() error
}

func run(ctx context.Context, outputDir string) error {
	return runWithGit(ctx, outputDir, gitOutput, os.Stdout)
}

func runWithGit(ctx context.Context, outputDir string, runGit gitCommand,
	stdout io.Writer) error {
	return runWithGitAndHooks(ctx, outputDir, runGit, stdout, regenerationHooks{})
}

func runWithGitAndHooks(ctx context.Context, outputDir string, runGit gitCommand,
	stdout io.Writer, hooks regenerationHooks) error {
	if outputDir == "" {
		return errors.New("-output is required")
	}
	if err := atomicPublicationSupported(); err != nil {
		return err
	}
	repoRoot, err := runGit(ctx, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	repoRoot = strings.TrimSpace(repoRoot)
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return fmt.Errorf("resolve output directory: %w", err)
	}
	frozenDir := filepath.Join(repoRoot, fixtureRelativeDir)
	if err = validateOutputDestination(frozenDir, outputDir); err != nil {
		return err
	}

	if err = validateProvenance(ctx, repoRoot, runGit); err != nil {
		return err
	}
	files, err := generateFixtureFiles(ctx, repoRoot, runGit)
	if err != nil {
		return err
	}

	// Revalidate immediately before reserving the destination because historical
	// generation may take long enough for the filesystem namespace to change.
	if err = validateOutputDestination(frozenDir, outputDir); err != nil {
		return err
	}
	if err = publishOutput(frozenDir, outputDir, files, hooks); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "reproduced format %d fixtures from %s (%s) using generator %s in %s\n",
		checkpointFormatVersion, producerCommit, producerVersion, generatorCommit, outputDir)
	return nil
}

func validateOutputDestination(frozenDir, outputDir string) error {
	withinFrozen, err := pathWithinResolvedDir(frozenDir, outputDir)
	if err != nil {
		return err
	}
	if withinFrozen {
		return fmt.Errorf("refusing to overwrite frozen fixtures at %s", frozenDir)
	}
	if _, err = os.Lstat(outputDir); err == nil {
		return outputExistsError{path: outputDir}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	return nil
}

type outputExistsError struct {
	path string
}

func (e outputExistsError) Error() string {
	return "output directory already exists: " + e.path
}

func (e outputExistsError) Unwrap() error {
	return os.ErrExist
}

func generateFixtureFiles(ctx context.Context, repoRoot string,
	runGit gitCommand) (files []generatedFixtureFile, retErr error) {
	tempDir, err := os.MkdirTemp("", "eino-checkpoint-compat-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary directory: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(tempDir); retErr == nil && cleanupErr != nil {
			retErr = fmt.Errorf("remove temporary directory: %w", cleanupErr)
		}
	}()
	worktreeDir := filepath.Join(tempDir, "worktree")
	if _, err = runGit(ctx, repoRoot, "worktree", "add", "--detach", worktreeDir,
		generatorCommit); err != nil {
		return nil, err
	}
	defer func() {
		if _, cleanupErr := runGit(context.Background(), repoRoot,
			"worktree", "remove", "--force", worktreeDir); retErr == nil && cleanupErr != nil {
			retErr = cleanupErr
		}
	}()

	generatedDir := filepath.Join(worktreeDir, fixtureRelativeDir)
	if err = annotateManifest(filepath.Join(generatedDir, "manifest.json")); err != nil {
		return nil, err
	}
	files, err = readGeneratedFixtureFiles(generatedDir)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func pathWithinResolvedDir(dir, path string) (bool, error) {
	resolvedDir, err := resolveExistingPathComponents(dir)
	if err != nil {
		return false, fmt.Errorf("resolve frozen fixture directory: %w", err)
	}
	resolvedPath, err := resolveExistingPathComponents(path)
	if err != nil {
		return false, fmt.Errorf("resolve output directory symlinks: %w", err)
	}
	relative, err := filepath.Rel(resolvedDir, resolvedPath)
	if err != nil {
		return false, fmt.Errorf("compare output with frozen fixture directory: %w", err)
	}
	if relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return true, nil
	}

	frozenInfo, err := os.Stat(resolvedDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect frozen fixture directory: %w", err)
	}
	existingPath, _, err := resolveNearestExistingPath(path)
	if err != nil {
		return false, fmt.Errorf("resolve existing output ancestor: %w", err)
	}
	for {
		info, statErr := os.Stat(existingPath)
		if statErr != nil {
			return false, fmt.Errorf("inspect existing output ancestor: %w", statErr)
		}
		if os.SameFile(frozenInfo, info) {
			return true, nil
		}
		parent := filepath.Dir(existingPath)
		if parent == existingPath {
			return false, nil
		}
		existingPath = parent
	}
}

func resolveExistingPathComponents(path string) (string, error) {
	resolved, missing, err := resolveNearestExistingPath(path)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return resolved, nil
}

func resolveNearestExistingPath(path string) (string, []string, error) {
	current := filepath.Clean(path)
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return "", nil, resolveErr
			}
			return resolved, missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func validateProvenance(ctx context.Context, repoRoot string, runGit gitCommand) error {
	parent, err := runGit(ctx, repoRoot, "rev-parse", generatorCommit+"^")
	if err != nil {
		return err
	}
	if strings.TrimSpace(parent) != producerCommit {
		return fmt.Errorf("generator %s is not based directly on producer %s",
			generatorCommit, producerCommit)
	}
	return nil
}

func annotateManifest(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read generated manifest: %w", err)
	}
	var generated historicalManifest
	if err = json.Unmarshal(data, &generated); err != nil {
		return fmt.Errorf("decode generated manifest: %w", err)
	}
	if generated.ProducerCommit != producerCommit {
		return fmt.Errorf("historical generator declared producer %q, want %q",
			generated.ProducerCommit, producerCommit)
	}
	for i := range generated.Fixtures {
		current := &generated.Fixtures[i]
		if current.Name == "parallel_6" {
			current.ImplicitResume = true
		}
		if err = validateFixtureHash(filepath.Dir(path), *current); err != nil {
			return err
		}
	}
	data, err = json.MarshalIndent(&manifest{
		ProducerCommit:          producerCommit,
		ProducerVersion:         producerVersion,
		GeneratorCommit:         generatorCommit,
		CheckpointFormatVersion: checkpointFormatVersion,
		Fixtures:                generated.Fixtures,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode generated manifest: %w", err)
	}
	if err = os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write generated manifest: %w", err)
	}
	return nil
}

func validateFixtureHash(dir string, current fixture) error {
	file, err := os.Open(filepath.Join(dir, current.File))
	if err != nil {
		return fmt.Errorf("open historical fixture %s: %w", current.File, err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open historical fixture gzip %s: %w", current.File, err)
	}
	defer reader.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, reader); err != nil {
		return fmt.Errorf("hash historical fixture %s: %w", current.File, err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != current.SHA256 {
		return fmt.Errorf("historical fixture %s hash = %s, want %s",
			current.File, actual, current.SHA256)
	}
	return nil
}

type generatedFixtureFile struct {
	name string
	data []byte
}

func readGeneratedFixtureFiles(source string) ([]generatedFixtureFile, error) {
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, fmt.Errorf("read generated fixtures: %w", err)
	}
	files := make([]generatedFixtureFile, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected non-regular fixture entry: %s", entry.Name())
		}
		data, readErr := os.ReadFile(filepath.Join(source, entry.Name()))
		if readErr != nil {
			return nil, fmt.Errorf("read generated fixture %s: %w", entry.Name(), readErr)
		}
		files = append(files, generatedFixtureFile{name: entry.Name(), data: data})
	}
	return files, nil
}

func validateGeneratedFixtureFiles(files []generatedFixtureFile) error {
	names := make(map[string]struct{}, len(files))
	for i := range files {
		name := files[i].name
		if name == "" || filepath.Base(name) != name || name == "." {
			return fmt.Errorf("invalid generated fixture name: %q", name)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("duplicate generated fixture name: %s", name)
		}
		names[name] = struct{}{}
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, output)
	}
	return string(output), nil
}
