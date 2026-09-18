//go:build linux || darwin
// +build linux darwin

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
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeGit struct {
	repoRoot       string
	parent         string
	materialize    func(string) error
	removeErr      error
	calls          [][]string
	worktreeDir    string
	worktreeRemove bool
}

func requireEqualError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func (f *fakeGit) run(_ context.Context, _ string, args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	switch {
	case reflect.DeepEqual(args, []string{"rev-parse", "--show-toplevel"}):
		return f.repoRoot + "\n", nil
	case reflect.DeepEqual(args, []string{"rev-parse", generatorCommit + "^"}):
		return f.parent + "\n", nil
	case len(args) == 5 && args[0] == "worktree" && args[1] == "add" &&
		args[2] == "--detach" && args[4] == generatorCommit:
		f.worktreeDir = args[3]
		if f.materialize != nil {
			return "", f.materialize(f.worktreeDir)
		}
		return "", nil
	case len(args) == 4 && args[0] == "worktree" && args[1] == "remove" &&
		args[2] == "--force":
		f.worktreeRemove = true
		return "", f.removeErr
	default:
		return "", fmt.Errorf("unexpected git command: %v", args)
	}
}

func TestRun(t *testing.T) {
	t.Run("requires output", func(t *testing.T) {
		err := runWithGit(context.Background(), "", (&fakeGit{}).run, io.Discard)
		requireEqualError(t, err, "-output is required")
	})

	t.Run("rejects frozen output", func(t *testing.T) {
		repoRoot := t.TempDir()
		fake := &fakeGit{repoRoot: repoRoot}
		outputDir := filepath.Join(repoRoot, fixtureRelativeDir)
		err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
		wantErr := "refusing to overwrite frozen fixtures at " + outputDir
		requireEqualError(t, err, wantErr)
		if len(fake.calls) != 1 {
			t.Fatalf("git calls = %v, want only repository discovery", fake.calls)
		}
	})

	t.Run("rejects frozen output descendant without modification", func(t *testing.T) {
		repoRoot := t.TempDir()
		frozenDir := filepath.Join(repoRoot, fixtureRelativeDir)
		if err := os.MkdirAll(frozenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(frozenDir, "sentinel")
		if err := os.WriteFile(sentinel, []byte("frozen"), 0o644); err != nil {
			t.Fatal(err)
		}
		fake := &fakeGit{repoRoot: repoRoot}
		outputDir := filepath.Join(frozenDir, "descendant")

		err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
		wantErr := "refusing to overwrite frozen fixtures at " + frozenDir
		requireEqualError(t, err, wantErr)
		if len(fake.calls) != 1 {
			t.Fatalf("git calls = %v, want only repository discovery", fake.calls)
		}
		assertFileContent(t, sentinel, "frozen")
		if _, statErr := os.Stat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("frozen descendant was created: %v", statErr)
		}
	})

	t.Run("rejects symlinked parent into frozen output without modification", func(t *testing.T) {
		repoRoot := t.TempDir()
		frozenDir := filepath.Join(repoRoot, fixtureRelativeDir)
		if err := os.MkdirAll(frozenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(frozenDir, "sentinel")
		if err := os.WriteFile(sentinel, []byte("frozen"), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "frozen-link")
		if err := os.Symlink(frozenDir, link); err != nil {
			t.Skipf("create symlink: %v", err)
		}
		fake := &fakeGit{repoRoot: repoRoot}
		outputDir := filepath.Join(link, "descendant")

		err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
		wantErr := "refusing to overwrite frozen fixtures at " + frozenDir
		requireEqualError(t, err, wantErr)
		if len(fake.calls) != 1 {
			t.Fatalf("git calls = %v, want only repository discovery", fake.calls)
		}
		assertFileContent(t, sentinel, "frozen")
		if _, statErr := os.Stat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("frozen descendant was created through symlink: %v", statErr)
		}
	})

	t.Run("rejects existing output", func(t *testing.T) {
		repoRoot := t.TempDir()
		outputDir := filepath.Join(t.TempDir(), "existing")
		if err := os.Mkdir(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		fake := &fakeGit{repoRoot: repoRoot}
		err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
		wantErr := "output directory already exists: " + outputDir
		requireEqualError(t, err, wantErr)
		if _, statErr := os.Stat(outputDir); statErr != nil {
			t.Fatalf("existing output was modified: %v", statErr)
		}
	})

	t.Run("requires generator direct parent provenance", func(t *testing.T) {
		fake := &fakeGit{
			repoRoot: t.TempDir(),
			parent:   "not-the-producer",
		}
		outputDir := filepath.Join(t.TempDir(), "output")
		var stdout bytes.Buffer
		err := runWithGit(context.Background(), outputDir, fake.run, &stdout)
		wantErr := "generator " + generatorCommit +
			" is not based directly on producer " + producerCommit
		requireEqualError(t, err, wantErr)
		wantCall := []string{"rev-parse", generatorCommit + "^"}
		if !containsCall(fake.calls, wantCall) {
			t.Fatalf("git calls = %v, want %v", fake.calls, wantCall)
		}
		assertFailedPublication(t, outputDir, stdout.String())
	})

	t.Run("rejects historical manifest with wrong producer", func(t *testing.T) {
		fake := newSuccessfulFakeGit(t)
		materialize := fake.materialize
		fake.materialize = func(worktreeDir string) error {
			if err := materialize(worktreeDir); err != nil {
				return err
			}
			path := filepath.Join(worktreeDir, fixtureRelativeDir, "manifest.json")
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var historical historicalManifest
			if err = json.Unmarshal(data, &historical); err != nil {
				return err
			}
			historical.ProducerCommit = "wrong-producer"
			data, err = json.MarshalIndent(&historical, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(path, append(data, '\n'), 0o644)
		}
		outputDir := filepath.Join(t.TempDir(), "output")
		var stdout bytes.Buffer

		err := runWithGit(context.Background(), outputDir, fake.run, &stdout)
		wantErr := `historical generator declared producer "wrong-producer", want "` +
			producerCommit + `"`
		requireEqualError(t, err, wantErr)
		assertFailedPublication(t, outputDir, stdout.String())
	})

	t.Run("rejects malformed historical manifest", func(t *testing.T) {
		fake := newSuccessfulFakeGit(t)
		materialize := fake.materialize
		fake.materialize = func(worktreeDir string) error {
			if err := materialize(worktreeDir); err != nil {
				return err
			}
			return os.WriteFile(
				filepath.Join(worktreeDir, fixtureRelativeDir, "manifest.json"),
				[]byte("not-json\n"), 0o644,
			)
		}
		outputDir := filepath.Join(t.TempDir(), "output")
		var stdout bytes.Buffer

		err := runWithGit(context.Background(), outputDir, fake.run, &stdout)
		wantErr := "decode generated manifest: invalid character 'o' in literal null (expecting 'u')"
		requireEqualError(t, err, wantErr)
		assertFailedPublication(t, outputDir, stdout.String())
	})

	t.Run("rejects decompressed historical fixture hash mismatch", func(t *testing.T) {
		fake := newSuccessfulFakeGit(t)
		materialize := fake.materialize
		fake.materialize = func(worktreeDir string) error {
			if err := materialize(worktreeDir); err != nil {
				return err
			}
			path := filepath.Join(worktreeDir, fixtureRelativeDir, "parallel_6.bin.gz")
			compressed, err := gzipBytes([]byte("different checkpoint"))
			if err != nil {
				return err
			}
			return os.WriteFile(path, compressed, 0o644)
		}
		outputDir := filepath.Join(t.TempDir(), "output")
		var stdout bytes.Buffer

		err := runWithGit(context.Background(), outputDir, fake.run, &stdout)
		actual := sha256.Sum256([]byte("different checkpoint"))
		want := sha256.Sum256([]byte("historical checkpoint"))
		wantErr := fmt.Sprintf("historical fixture parallel_6.bin.gz hash = %s, want %s",
			hex.EncodeToString(actual[:]), hex.EncodeToString(want[:]))
		requireEqualError(t, err, wantErr)
		assertFailedPublication(t, outputDir, stdout.String())
	})

	t.Run("atomic no-replace preserves output created before publication", func(t *testing.T) {
		fake := newSuccessfulFakeGit(t)
		outputDir := filepath.Join(t.TempDir(), "output")
		materialize := fake.materialize
		fake.materialize = func(worktreeDir string) error {
			if err := materialize(worktreeDir); err != nil {
				return err
			}
			return os.Mkdir(outputDir, 0o755)
		}
		err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
		if !errors.Is(err, os.ErrExist) {
			t.Fatalf("error = %v, want errors.Is(os.ErrExist)", err)
		}
		if _, statErr := os.Stat(outputDir); statErr != nil {
			t.Fatalf("pre-publication output was removed: %v", statErr)
		}
	})

	t.Run("does not create output for invalid generated entry", func(t *testing.T) {
		fake := newSuccessfulFakeGit(t)
		materialize := fake.materialize
		fake.materialize = func(worktreeDir string) error {
			if err := materialize(worktreeDir); err != nil {
				return err
			}
			return os.Mkdir(filepath.Join(worktreeDir, fixtureRelativeDir, "z-unexpected"), 0o755)
		}
		outputDir := filepath.Join(t.TempDir(), "output")

		err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
		wantErr := "unexpected non-regular fixture entry: z-unexpected"
		requireEqualError(t, err, wantErr)
		if _, statErr := os.Stat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("partial output exists after failed copy: %v", statErr)
		}
	})

	t.Run("copies annotated fixtures", func(t *testing.T) {
		fake := newSuccessfulFakeGit(t)
		outputDir := filepath.Join(t.TempDir(), "output")
		var stdout bytes.Buffer
		if err := runWithGit(context.Background(), outputDir, fake.run, &stdout); err != nil {
			t.Fatal(err)
		}
		wantStdout := fmt.Sprintf(
			"reproduced format %d fixtures from %s (%s) using generator %s in %s\n",
			checkpointFormatVersion, producerCommit, producerVersion, generatorCommit, outputDir)
		if stdout.String() != wantStdout {
			t.Fatalf("stdout = %q, want %q", stdout.String(), wantStdout)
		}
		if !fake.worktreeRemove {
			t.Fatal("historical worktree was not removed")
		}
		assertAnnotatedOutput(t, outputDir)
		if _, err := os.Stat(fake.worktreeDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary worktree still exists: %v", err)
		}
	})

	t.Run("removes output when final cleanup fails", func(t *testing.T) {
		fake := newSuccessfulFakeGit(t)
		removeErr := errors.New("remove worktree failed")
		fake.removeErr = removeErr
		outputDir := filepath.Join(t.TempDir(), "output")
		err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
		if !errors.Is(err, removeErr) {
			t.Fatalf("error = %v, want %v", err, removeErr)
		}
		if _, statErr := os.Stat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("output exists after failed command: %v", statErr)
		}
		if _, statErr := os.Stat(fake.worktreeDir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("temporary worktree still exists: %v", statErr)
		}
	})
}

func TestRunSymlinkSwapBeforeOutputReservation(t *testing.T) {
	repoRoot := t.TempDir()
	frozenDir := filepath.Join(repoRoot, fixtureRelativeDir)
	unownedOutput := filepath.Join(frozenDir, "output")
	if err := os.MkdirAll(unownedOutput, 0o755); err != nil {
		t.Fatal(err)
	}
	unownedSentinel := filepath.Join(unownedOutput, "sentinel")
	if err := os.WriteFile(unownedSentinel, []byte("unowned"), 0o644); err != nil {
		t.Fatal(err)
	}
	frozenBefore := readDirectoryFiles(t, frozenDir)

	externalRoot := t.TempDir()
	requestedParent := filepath.Join(externalRoot, "requested-parent")
	if err := os.Mkdir(requestedParent, 0o755); err != nil {
		t.Fatal(err)
	}
	probeLink := filepath.Join(externalRoot, "symlink-probe")
	if err := os.Symlink(frozenDir, probeLink); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if err := os.Remove(probeLink); err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(requestedParent, "output")
	reservedParent := filepath.Join(externalRoot, "reserved-parent")
	fake := newSuccessfulFakeGit(t)
	fake.repoRoot = repoRoot
	hooks := regenerationHooks{beforeOutputReserved: func() {
		if err := os.Rename(requestedParent, reservedParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(frozenDir, requestedParent); err != nil {
			t.Fatal(err)
		}
	}}

	err := runWithGitAndHooks(context.Background(), outputDir, fake.run, io.Discard, hooks)
	wantErr := "refusing to overwrite frozen fixtures at " + frozenDir
	requireEqualError(t, err, wantErr)
	assertDirectoryFilesEqual(t, frozenDir, frozenBefore)
	assertFileContent(t, unownedSentinel, "unowned")
	assertNoOutputArtifacts(t, filepath.Join(reservedParent, "output"))
}

func TestRunRejectsCaseVariantFrozenOutputOnCaseInsensitiveFilesystem(t *testing.T) {
	repoRoot := t.TempDir()
	frozenDir := filepath.Join(repoRoot, fixtureRelativeDir)
	if err := os.MkdirAll(frozenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !filesystemIsCaseInsensitive(t, filepath.Dir(frozenDir)) {
		t.Skip("filesystem is case-sensitive")
	}
	sentinel := filepath.Join(frozenDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("frozen"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGit{repoRoot: repoRoot}
	caseVariant := strings.ToUpper(filepath.Base(frozenDir))
	outputDir := filepath.Join(filepath.Dir(frozenDir), caseVariant, "descendant")

	err := runWithGit(context.Background(), outputDir, fake.run, io.Discard)
	wantErr := "refusing to overwrite frozen fixtures at " + frozenDir
	requireEqualError(t, err, wantErr)
	if len(fake.calls) != 1 {
		t.Fatalf("git calls = %v, want only repository discovery", fake.calls)
	}
	assertFileContent(t, sentinel, "frozen")
	if _, statErr := os.Stat(outputDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("case-variant frozen descendant was created: %v", statErr)
	}
}

func TestRunAllowsCaseVariantFrozenOutputOnCaseSensitiveFilesystem(t *testing.T) {
	repoRoot := t.TempDir()
	frozenDir := filepath.Join(repoRoot, fixtureRelativeDir)
	if err := os.MkdirAll(frozenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if filesystemIsCaseInsensitive(t, filepath.Dir(frozenDir)) {
		t.Skip("filesystem is case-insensitive")
	}
	sentinel := filepath.Join(frozenDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("frozen"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := newSuccessfulFakeGit(t)
	fake.repoRoot = repoRoot
	outputDir := filepath.Join(filepath.Dir(frozenDir), strings.ToUpper(filepath.Base(frozenDir)))

	if err := runWithGit(context.Background(), outputDir, fake.run, io.Discard); err != nil {
		t.Fatal(err)
	}
	assertAnnotatedOutput(t, outputDir)
	assertFileContent(t, sentinel, "frozen")
}

func TestRunSymlinkSwapAfterOutputReservation(t *testing.T) {
	repoRoot := t.TempDir()
	frozenDir := filepath.Join(repoRoot, fixtureRelativeDir)
	unownedOutput := filepath.Join(frozenDir, "output")
	if err := os.MkdirAll(unownedOutput, 0o755); err != nil {
		t.Fatal(err)
	}
	unownedSentinel := filepath.Join(unownedOutput, "sentinel")
	if err := os.WriteFile(unownedSentinel, []byte("unowned"), 0o644); err != nil {
		t.Fatal(err)
	}
	frozenBefore := readDirectoryFiles(t, frozenDir)

	externalRoot := t.TempDir()
	requestedParent := filepath.Join(externalRoot, "requested-parent")
	if err := os.Mkdir(requestedParent, 0o755); err != nil {
		t.Fatal(err)
	}
	probeLink := filepath.Join(externalRoot, "symlink-probe")
	if err := os.Symlink(frozenDir, probeLink); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if err := os.Remove(probeLink); err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(requestedParent, "output")
	reservedParent := filepath.Join(externalRoot, "reserved-parent")
	fake := newSuccessfulFakeGit(t)
	fake.repoRoot = repoRoot
	hooks := regenerationHooks{afterOutputReserved: func() {
		if err := os.Rename(requestedParent, reservedParent); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(frozenDir, requestedParent); err != nil {
			t.Fatal(err)
		}
	}}

	err := runWithGitAndHooks(context.Background(), outputDir, fake.run, io.Discard, hooks)
	wantErr := "output directory identity changed after reservation: " + outputDir
	requireEqualError(t, err, wantErr)
	frozenAfter := readDirectoryFiles(t, frozenDir)
	if !reflect.DeepEqual(frozenAfter, frozenBefore) {
		t.Fatalf("frozen fixtures changed:\ngot:  %#v\nwant: %#v", frozenAfter, frozenBefore)
	}
	assertFileContent(t, unownedSentinel, "unowned")
	assertNoOutputArtifacts(t, filepath.Join(reservedParent, "output"))
}

func TestRunPublicationFailureIsAtomic(t *testing.T) {
	fake := newSuccessfulFakeGit(t)
	outputDir := filepath.Join(t.TempDir(), "output")
	publishErr := errors.New("injected publication failure")
	var stdout bytes.Buffer
	hooks := regenerationHooks{
		publishOutput: func() error {
			if _, err := os.Lstat(outputDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("output became visible before atomic publication: %v", err)
			}
			return publishErr
		},
	}

	err := runWithGitAndHooks(context.Background(), outputDir, fake.run, &stdout, hooks)
	if !errors.Is(err, publishErr) {
		t.Fatalf("error = %v, want %v", err, publishErr)
	}
	assertFailedPublication(t, outputDir, stdout.String())
}

func TestValidateFixtureHash(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("decompressed checkpoint bytes")
	file := "fixture.bin.gz"
	compressed := writeGzipFile(t, filepath.Join(dir, file), raw)
	decompressedSum := sha256.Sum256(raw)
	compressedSum := sha256.Sum256(compressed)
	if decompressedSum == compressedSum {
		t.Fatal("test fixture does not distinguish compressed and decompressed hashes")
	}
	current := fixture{File: file, SHA256: hex.EncodeToString(decompressedSum[:])}
	if err := validateFixtureHash(dir, current); err != nil {
		t.Fatalf("decompressed hash rejected: %v", err)
	}

	current.SHA256 = strings.Repeat("0", sha256.Size*2)
	err := validateFixtureHash(dir, current)
	wantErr := fmt.Sprintf("historical fixture %s hash = %s, want %s",
		file, hex.EncodeToString(decompressedSum[:]), current.SHA256)
	requireEqualError(t, err, wantErr)

	t.Run("missing fixture", func(t *testing.T) {
		missing := fixture{File: "missing.bin.gz"}
		err := validateFixtureHash(dir, missing)
		if err == nil || !strings.Contains(
			err.Error(), "open historical fixture missing.bin.gz:",
		) {
			t.Fatalf("error = %v, want missing fixture error", err)
		}
	})

	t.Run("invalid gzip header", func(t *testing.T) {
		name := "invalid.bin.gz"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not gzip"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := validateFixtureHash(dir, fixture{File: name})
		if err == nil || !strings.Contains(
			err.Error(), "open historical fixture gzip invalid.bin.gz:",
		) {
			t.Fatalf("error = %v, want invalid gzip error", err)
		}
	})

	t.Run("truncated gzip payload", func(t *testing.T) {
		name := "truncated.bin.gz"
		compressed, err := gzipBytes([]byte("payload"))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(
			filepath.Join(dir, name), compressed[:len(compressed)-4], 0o644,
		); err != nil {
			t.Fatal(err)
		}
		err = validateFixtureHash(dir, fixture{File: name})
		if err == nil || !strings.Contains(
			err.Error(), "hash historical fixture truncated.bin.gz:",
		) {
			t.Fatalf("error = %v, want truncated gzip error", err)
		}
	})
}

func TestAnnotateManifest(t *testing.T) {
	dir := t.TempDir()
	raw := []byte("fixture payload")
	file := "parallel.bin.gz"
	writeGzipFile(t, filepath.Join(dir, file), raw)
	sum := sha256.Sum256(raw)
	historical := historicalManifest{
		ProducerCommit: producerCommit,
		Fixtures: []fixture{{
			Name:   "parallel_6",
			File:   file,
			SHA256: hex.EncodeToString(sum[:]),
		}},
	}
	writeJSONFile(t, filepath.Join(dir, "manifest.json"), historical)

	if err := annotateManifest(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("annotated manifest must end with a newline")
	}
	var annotated manifest
	if err = json.Unmarshal(data, &annotated); err != nil {
		t.Fatal(err)
	}
	if annotated.ProducerCommit != producerCommit ||
		annotated.ProducerVersion != producerVersion ||
		annotated.GeneratorCommit != generatorCommit ||
		annotated.CheckpointFormatVersion != checkpointFormatVersion {
		t.Fatalf("unexpected provenance annotation: %#v", annotated)
	}
	if len(annotated.Fixtures) != 1 || !annotated.Fixtures[0].ImplicitResume {
		t.Fatalf("parallel_6 annotation = %#v, want implicit resume", annotated.Fixtures)
	}
}

func TestValidateProvenanceAgainstRepository(t *testing.T) {
	repoRoot, err := gitOutput(context.Background(), "", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateProvenance(context.Background(), strings.TrimSpace(repoRoot), gitOutput); err != nil {
		t.Fatal(err)
	}
}

func TestRunHistoricalRegeneration(t *testing.T) {
	if os.Getenv("EINO_RUN_HISTORICAL_REGENERATION") != "1" {
		t.Skip("set EINO_RUN_HISTORICAL_REGENERATION=1 to create a historical worktree")
	}
	outputDir := filepath.Join(t.TempDir(), "regenerated")
	if err := run(context.Background(), outputDir); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := gitOutput(context.Background(), "", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	assertDirectoriesEqual(t, filepath.Join(strings.TrimSpace(repoRoot), fixtureRelativeDir), outputDir)
}

func TestReadGeneratedFixtureFiles(t *testing.T) {
	t.Run("reads regular files", func(t *testing.T) {
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "fixture"), []byte("checkpoint"), 0o644); err != nil {
			t.Fatal(err)
		}

		files, err := readGeneratedFixtureFiles(source)
		if err != nil {
			t.Fatal(err)
		}
		want := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
		if !reflect.DeepEqual(files, want) {
			t.Fatalf("files = %#v, want %#v", files, want)
		}
	})

	t.Run("rejects non-regular entry", func(t *testing.T) {
		source := t.TempDir()
		if err := os.Mkdir(filepath.Join(source, "z-unexpected"), 0o755); err != nil {
			t.Fatal(err)
		}

		_, err := readGeneratedFixtureFiles(source)
		wantErr := "unexpected non-regular fixture entry: z-unexpected"
		requireEqualError(t, err, wantErr)
	})

	t.Run("reports missing source", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "missing")
		_, err := readGeneratedFixtureFiles(source)
		if err == nil || !strings.Contains(err.Error(), "read generated fixtures:") {
			t.Fatalf("error = %v, want source read error", err)
		}
	})
}

func TestValidateGeneratedFixtureFiles(t *testing.T) {
	tests := []struct {
		name    string
		files   []generatedFixtureFile
		wantErr string
	}{
		{
			name:    "empty name",
			files:   []generatedFixtureFile{{name: ""}},
			wantErr: `invalid generated fixture name: ""`,
		},
		{
			name:    "dot name",
			files:   []generatedFixtureFile{{name: "."}},
			wantErr: `invalid generated fixture name: "."`,
		},
		{
			name: "duplicate name",
			files: []generatedFixtureFile{
				{name: "fixture.bin.gz"},
				{name: "fixture.bin.gz"},
			},
			wantErr: "duplicate generated fixture name: fixture.bin.gz",
		},
		{
			name:  "unique names",
			files: []generatedFixtureFile{{name: "a"}, {name: "b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGeneratedFixtureFiles(tt.files)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("error = %v, want nil", err)
				}
				return
			}
			requireEqualError(t, err, tt.wantErr)
		})
	}
}

func TestPublishOutput(t *testing.T) {
	t.Run("writes reserved files", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "output")
		files := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
		if err := publishOutput("", destination, files, regenerationHooks{}); err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, filepath.Join(destination, "fixture"), "checkpoint")
	})

	t.Run("does not overwrite existing destination", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "output")
		if err := os.Mkdir(destination, 0o755); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(destination, "sentinel")
		if err := os.WriteFile(sentinel, []byte("existing"), 0o644); err != nil {
			t.Fatal(err)
		}

		err := publishOutput("", destination, nil, regenerationHooks{})
		wantErr := "output directory already exists: " + destination
		requireEqualError(t, err, wantErr)
		assertFileContent(t, sentinel, "existing")
	})

	t.Run("rejects invalid names before creating destination", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "output")
		files := []generatedFixtureFile{{name: "../fixture", data: []byte("checkpoint")}}
		err := publishOutput("", destination, files, regenerationHooks{})
		wantErr := `invalid generated fixture name: "../fixture"`
		requireEqualError(t, err, wantErr)
		if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("destination was created: %v", statErr)
		}
	})

	t.Run("removes staging after write failure", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "output")
		writeErr := errors.New("injected write failure")
		files := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
		hooks := regenerationHooks{
			writeOutputFile: func(_ string, writer io.Writer, data []byte) error {
				if _, err := writer.Write(data[:1]); err != nil {
					return err
				}
				return writeErr
			},
		}

		err := publishOutput("", destination, files, hooks)
		if !errors.Is(err, writeErr) {
			t.Fatalf("error = %v, want %v", err, writeErr)
		}
		assertNoOutputArtifacts(t, destination)
	})

	t.Run("removes staging after close failure", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "output")
		closeErr := errors.New("injected close failure")
		files := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
		hooks := regenerationHooks{
			closeOutputFile: func(_ string, file *os.File) error {
				if err := file.Close(); err != nil {
					return err
				}
				return closeErr
			},
		}

		err := publishOutput("", destination, files, hooks)
		if !errors.Is(err, closeErr) {
			t.Fatalf("error = %v, want %v", err, closeErr)
		}
		assertNoOutputArtifacts(t, destination)
	})

	t.Run("does not replace destination created before publication", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "output")
		sentinel := filepath.Join(destination, "sentinel")
		files := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
		hooks := regenerationHooks{
			beforeOutputPublished: func() {
				if err := os.Mkdir(destination, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sentinel, []byte("unowned"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		}

		err := publishOutput("", destination, files, hooks)
		wantErr := "output directory already exists: " + destination
		requireEqualError(t, err, wantErr)
		assertFileContent(t, sentinel, "unowned")
		assertNoStagingDirectories(t, filepath.Dir(destination))
	})
}

func TestPublishOutputCleansPartialStageReservation(t *testing.T) {
	faultErr := errors.New("injected stage reservation failure")
	tests := []struct {
		name  string
		hooks regenerationHooks
	}{
		{
			name: "entry identity failure",
		},
		{
			name: "open failure",
		},
		{
			name: "descriptor identity failure",
		},
		{name: "post-open failure"},
		{name: "stat failure"},
	}
	tests[0].hooks.inspectOutputStageEntry = func(int, string) (directoryIdentity, error) {
		return directoryIdentity{}, faultErr
	}
	tests[1].hooks.openOutputStage = func(int, string) (*os.File, error) {
		return nil, faultErr
	}
	tests[2].hooks.inspectOutputStageDescriptor = func(
		*os.File) (directoryIdentity, error) {
		return directoryIdentity{}, faultErr
	}
	tests[3].hooks.afterOutputStageOpened = func(int, string) error {
		return faultErr
	}
	tests[4].hooks.inspectOutputStage = func(*os.File) (os.FileInfo, error) {
		return nil, faultErr
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			destination := filepath.Join(parent, "output")

			err := publishOutput("", destination, nil, tt.hooks)
			if !errors.Is(err, faultErr) {
				t.Fatalf("error = %v, want %v", err, faultErr)
			}
			assertNoOutputArtifacts(t, destination)
		})
	}
}

func TestPublishOutputCleansPartialCandidateReservation(t *testing.T) {
	faultErr := errors.New("injected candidate reservation failure")
	tests := []struct {
		name  string
		hooks regenerationHooks
	}{
		{
			name: "mkdir failure",
			hooks: regenerationHooks{
				makeOutputCandidate: func(int, string) error {
					return faultErr
				},
			},
		},
		{
			name: "identity failure",
			hooks: regenerationHooks{
				inspectOutputCandidateEntry: func(int, string) (directoryIdentity, error) {
					return directoryIdentity{}, faultErr
				},
			},
		},
		{
			name: "open failure",
			hooks: regenerationHooks{
				openOutputCandidate: func(int, string) (*os.File, error) {
					return nil, faultErr
				},
			},
		},
		{
			name: "descriptor identity failure",
			hooks: regenerationHooks{
				inspectOutputCandidateDescriptor: func(
					*os.File) (directoryIdentity, error) {
					return directoryIdentity{}, faultErr
				},
			},
		},
		{
			name: "post-open failure",
			hooks: regenerationHooks{
				afterOutputCandidateOpened: func(int, string) error {
					return faultErr
				},
			},
		},
		{
			name: "stat failure",
			hooks: regenerationHooks{
				inspectOutputCandidate: func(*os.File) (os.FileInfo, error) {
					return nil, faultErr
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			destination := filepath.Join(parent, "output")

			err := publishOutput("", destination, nil, tt.hooks)
			if !errors.Is(err, faultErr) {
				t.Fatalf("error = %v, want %v", err, faultErr)
			}
			assertNoOutputArtifacts(t, destination)
		})
	}
}

func TestStageReplacementAfterFirstEntryIdentityCaptureIsNotAdopted(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		fail      bool
	}{
		{name: "open", operation: "open"},
		{name: "open failure", operation: "open", fail: true},
		{name: "descriptor identity", operation: "descriptor identity"},
		{name: "descriptor identity failure", operation: "descriptor identity", fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			destination := filepath.Join(parent, "output")
			renamedStage := filepath.Join(parent, "renamed-owned-stage")
			replacementSentinel := ""
			faultErr := errors.New("injected " + tt.name)
			replaceStage := func(stageName string) {
				stagePath := filepath.Join(parent, stageName)
				if err := os.Rename(stagePath, renamedStage); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(stagePath, 0o755); err != nil {
					t.Fatal(err)
				}
				replacementSentinel = filepath.Join(stagePath, "unowned")
				if err := os.WriteFile(
					replacementSentinel, []byte("replacement"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			hooks := regenerationHooks{}
			if tt.operation == "open" {
				hooks.openOutputStage = func(parentFD int, stageName string) (*os.File, error) {
					replaceStage(stageName)
					if tt.fail {
						return nil, faultErr
					}
					return openDirectoryAt(parentFD, stageName)
				}
			} else {
				hooks.inspectOutputStageDescriptor = func(
					stage *os.File) (directoryIdentity, error) {
					replaceStage(filepath.Base(stage.Name()))
					if tt.fail {
						return directoryIdentity{}, faultErr
					}
					return directoryDescriptorIdentity(stage)
				}
			}

			err := publishOutput("", destination, nil, hooks)
			if tt.fail {
				if !errors.Is(err, faultErr) {
					t.Fatalf("error = %v, want %v", err, faultErr)
				}
			} else {
				wantErr := "output staging directory identity changed after creation"
				requireEqualError(t, err, wantErr)
			}
			if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("final output exists after stage replacement: %v", statErr)
			}
			assertFileContent(t, replacementSentinel, "replacement")
			assertDirectoryEmpty(t, renamedStage)
		})
	}
}

func TestCandidateReplacementAfterFirstEntryIdentityCaptureIsNotAdopted(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		fail      bool
	}{
		{name: "open", operation: "open"},
		{name: "open failure", operation: "open", fail: true},
		{name: "descriptor identity", operation: "descriptor identity"},
		{name: "descriptor identity failure", operation: "descriptor identity", fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			destination := filepath.Join(parent, "output")
			renamedCandidate := ""
			replacementSentinel := ""
			faultErr := errors.New("injected " + tt.name)
			replaceCandidate := func() {
				stagePath := findOnlyStagingDirectory(t, parent)
				candidatePath := filepath.Join(stagePath, stagedOutputName)
				renamedCandidate = filepath.Join(stagePath, "renamed-owned-candidate")
				if err := os.Rename(candidatePath, renamedCandidate); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(candidatePath, 0o755); err != nil {
					t.Fatal(err)
				}
				replacementSentinel = filepath.Join(candidatePath, "unowned")
				if err := os.WriteFile(
					replacementSentinel, []byte("replacement"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			hooks := regenerationHooks{}
			if tt.operation == "open" {
				hooks.openOutputCandidate = func(
					parentFD int, candidateName string) (*os.File, error) {
					replaceCandidate()
					if tt.fail {
						return nil, faultErr
					}
					return openDirectoryAt(parentFD, candidateName)
				}
			} else {
				hooks.inspectOutputCandidateDescriptor = func(
					candidate *os.File) (directoryIdentity, error) {
					replaceCandidate()
					if tt.fail {
						return directoryIdentity{}, faultErr
					}
					return directoryDescriptorIdentity(candidate)
				}
			}

			err := publishOutput("", destination, nil, hooks)
			if tt.fail {
				if !errors.Is(err, faultErr) {
					t.Fatalf("error = %v, want %v", err, faultErr)
				}
			} else if err == nil || !strings.Contains(
				err.Error(), "staged output directory identity changed after creation",
			) {
				t.Fatalf("error = %v, want candidate identity failure", err)
			}
			if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("final output exists after candidate replacement: %v", statErr)
			}
			assertFileContent(t, replacementSentinel, "replacement")
			assertDirectoryEmpty(t, renamedCandidate)
		})
	}
}

func TestCandidateReservationCleanupHonorsIdentityOwnership(t *testing.T) {
	for _, failurePoint := range []string{"identity", "open", "stat"} {
		t.Run(failurePoint, func(t *testing.T) {
			parent := t.TempDir()
			destination := filepath.Join(parent, "output")
			faultErr := errors.New("injected candidate " + failurePoint + " failure")
			renamedCandidate := ""
			replacementSentinel := ""
			replaceCandidate := func() {
				stagePath := findOnlyStagingDirectory(t, parent)
				candidatePath := filepath.Join(stagePath, stagedOutputName)
				if failurePoint == "identity" {
					if err := os.Remove(candidatePath); err != nil {
						t.Fatal(err)
					}
				} else {
					renamedCandidate = filepath.Join(stagePath, "renamed-owned-candidate")
					if err := os.Rename(candidatePath, renamedCandidate); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(candidatePath, 0o755); err != nil {
					t.Fatal(err)
				}
				if failurePoint != "identity" {
					replacementSentinel = filepath.Join(candidatePath, "unowned")
					if err := os.WriteFile(
						replacementSentinel, []byte("replacement"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			hooks := regenerationHooks{}
			if failurePoint == "identity" {
				hooks.inspectOutputCandidateEntry = func(int, string) (directoryIdentity, error) {
					replaceCandidate()
					return directoryIdentity{}, faultErr
				}
			} else if failurePoint == "open" {
				hooks.afterOutputCandidateOpened = func(int, string) error {
					replaceCandidate()
					return faultErr
				}
			} else {
				hooks.inspectOutputCandidate = func(*os.File) (os.FileInfo, error) {
					replaceCandidate()
					return nil, faultErr
				}
			}

			err := publishOutput("", destination, nil, hooks)
			if !errors.Is(err, faultErr) {
				t.Fatalf("error = %v, want %v", err, faultErr)
			}
			if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("final output exists after reservation failure: %v", statErr)
			}
			if failurePoint == "identity" {
				assertDirectoryEmpty(t, filepath.Join(
					findOnlyStagingDirectory(t, parent), stagedOutputName))
			} else {
				assertFileContent(t, replacementSentinel, "replacement")
				assertDirectoryEmpty(t, renamedCandidate)
			}
		})
	}
}

func TestStageReservationCleanupHonorsIdentityOwnership(t *testing.T) {
	for _, failurePoint := range []string{"identity", "open", "stat"} {
		t.Run(failurePoint, func(t *testing.T) {
			parent := t.TempDir()
			destination := filepath.Join(parent, "output")
			faultErr := errors.New("injected stage " + failurePoint + " failure")
			replacementSentinel := ""
			replaceStage := func(stageName string) {
				stagePath := filepath.Join(parent, stageName)
				if err := os.Remove(stagePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(stagePath, 0o755); err != nil {
					t.Fatal(err)
				}
				if failurePoint != "identity" {
					replacementSentinel = filepath.Join(stagePath, "unowned")
					if err := os.WriteFile(
						replacementSentinel, []byte("replacement"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			hooks := regenerationHooks{}
			if failurePoint == "identity" {
				hooks.inspectOutputStageEntry = func(_ int, stageName string) (
					directoryIdentity, error) {
					replaceStage(stageName)
					return directoryIdentity{}, faultErr
				}
			} else if failurePoint == "open" {
				hooks.afterOutputStageOpened = func(_ int, stageName string) error {
					replaceStage(stageName)
					return faultErr
				}
			} else {
				hooks.inspectOutputStage = func(stage *os.File) (os.FileInfo, error) {
					replaceStage(filepath.Base(stage.Name()))
					return nil, faultErr
				}
			}

			err := publishOutput("", destination, nil, hooks)
			if !errors.Is(err, faultErr) {
				t.Fatalf("error = %v, want %v", err, faultErr)
			}
			if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("final output exists after reservation failure: %v", statErr)
			}
			if failurePoint == "identity" {
				assertDirectoryEmpty(t, findOnlyStagingDirectory(t, parent))
			} else {
				assertFileContent(t, replacementSentinel, "replacement")
			}
		})
	}
}

func TestPublishOutputRejectsStageEntryReplacement(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "output")
	renamedStage := filepath.Join(parent, "renamed-owned-stage")
	replacementSentinel := ""
	files := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
	hooks := regenerationHooks{
		afterOutputReserved: func() {
			stagePath := findOnlyStagingDirectory(t, parent)
			if err := os.Rename(stagePath, renamedStage); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(stagePath, 0o755); err != nil {
				t.Fatal(err)
			}
			replacementSentinel = filepath.Join(stagePath, "attacker")
			if err := os.WriteFile(replacementSentinel, []byte("unowned"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	err := publishOutput("", destination, files, hooks)
	wantErr := "output staging directory identity changed before publication"
	requireEqualError(t, err, wantErr)
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output exists after stage replacement: %v", statErr)
	}
	assertFileContent(t, replacementSentinel, "unowned")
	assertDirectoryEmpty(t, renamedStage)
}

func TestPublishOutputCleansOwnedStageWithoutRemovingReplacement(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "output")
	renamedStage := filepath.Join(parent, "renamed-owned-stage")
	replacementSentinel := ""
	publishErr := errors.New("injected publication failure")
	files := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
	hooks := regenerationHooks{
		afterOutputReserved: func() {
			stagePath := findOnlyStagingDirectory(t, parent)
			if err := os.Rename(stagePath, renamedStage); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(stagePath, 0o755); err != nil {
				t.Fatal(err)
			}
			replacementSentinel = filepath.Join(stagePath, "attacker")
			if err := os.WriteFile(replacementSentinel, []byte("unowned"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		publishOutput: func() error {
			return publishErr
		},
	}

	err := publishOutput("", destination, files, hooks)
	if !errors.Is(err, publishErr) {
		t.Fatalf("error = %v, want %v", err, publishErr)
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output exists after failed publication: %v", statErr)
	}
	assertFileContent(t, replacementSentinel, "unowned")
	assertDirectoryEmpty(t, renamedStage)
}

func TestPublishOutputRejectsCandidateEntryReplacement(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "output")
	renamedCandidate := ""
	replacementSentinel := ""
	files := []generatedFixtureFile{{name: "fixture", data: []byte("checkpoint")}}
	hooks := regenerationHooks{
		beforeOutputPublished: func() {
			stagePath := findOnlyStagingDirectory(t, parent)
			candidatePath := filepath.Join(stagePath, stagedOutputName)
			renamedCandidate = filepath.Join(stagePath, "renamed-owned-candidate")
			if err := os.Rename(candidatePath, renamedCandidate); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(candidatePath, 0o755); err != nil {
				t.Fatal(err)
			}
			replacementSentinel = filepath.Join(candidatePath, "attacker")
			if err := os.WriteFile(replacementSentinel, []byte("unowned"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}

	err := publishOutput("", destination, files, hooks)
	if err == nil || !strings.Contains(
		err.Error(), "output staging candidate identity changed before publication",
	) {
		t.Fatalf("error = %v, want candidate identity failure", err)
	}
	if _, statErr := os.Lstat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("replacement candidate was published: %v", statErr)
	}
	assertFileContent(t, replacementSentinel, "unowned")
	assertDirectoryEmpty(t, renamedCandidate)
}

func TestAtomicPublicationHelpersFailClosed(t *testing.T) {
	t.Run("reservation rejects invalid destination", func(t *testing.T) {
		_, err := reserveAtomicOutput("", string(filepath.Separator))
		wantErr := "invalid output directory: " + string(filepath.Separator)
		requireEqualError(t, err, wantErr)
	})

	t.Run("reservation rejects missing parent", func(t *testing.T) {
		destination := filepath.Join(t.TempDir(), "missing", "output")
		_, err := reserveAtomicOutput("", destination)
		if err == nil || !strings.Contains(err.Error(), "open output parent directory:") {
			t.Fatalf("error = %v, want output parent error", err)
		}
	})

	t.Run("create stage rejects closed parent descriptor", func(t *testing.T) {
		parent, err := openDirectoryNoFollow(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err = parent.Close(); err != nil {
			t.Fatal(err)
		}
		publication := &atomicOutputPublication{parent: parent}
		err = publication.createStage()
		if err == nil || !strings.Contains(err.Error(), "reserve output staging directory") {
			t.Fatalf("error = %v, want staging reservation failure", err)
		}
	})

	t.Run("empty candidate operations are rejected or harmless", func(t *testing.T) {
		publication := &atomicOutputPublication{}
		if err := publication.sealCandidate(); err != nil {
			t.Fatalf("seal absent candidate: %v", err)
		}
		err := publication.validateCandidateIdentity()
		requireEqualError(t, err, "output staging candidate is not reserved")
		err = publication.validateStageIdentity()
		requireEqualError(t, err, "output staging directory is not reserved")
	})

	t.Run("closed candidate fails sealing and identity validation", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		if err = publication.candidate.Close(); err != nil {
			t.Fatal(err)
		}
		err = publication.sealCandidate()
		if err == nil || !strings.Contains(err.Error(), "set output directory permissions:") {
			t.Fatalf("error = %v, want candidate permission error", err)
		}
		err = publication.validateCandidateIdentity()
		requireEqualError(t, err,
			"output staging candidate identity changed before publication")
		publication.candidate = nil
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})

	t.Run("closed stage fails identity validation and cleanup preserves its entry", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		stagePath := filepath.Join(parent, publication.stageName)
		if err = publication.candidate.Close(); err != nil {
			t.Fatal(err)
		}
		publication.candidate = nil
		if err = publication.stage.Close(); err != nil {
			t.Fatal(err)
		}
		err = publication.validateStageIdentity()
		requireEqualError(t, err,
			"output staging directory identity changed before publication")
		publication.stage = nil
		if err = publication.closeAndCleanup(); err == nil ||
			!strings.Contains(err.Error(), "remove output staging directory:") {
			t.Fatalf("cleanup error = %v, want non-empty stage cleanup error", err)
		}
		if _, err = os.Stat(stagePath); err != nil {
			t.Fatalf("stage entry was removed without a usable descriptor: %v", err)
		}
	})

	t.Run("frozen path must be a directory", func(t *testing.T) {
		frozen := filepath.Join(t.TempDir(), "frozen")
		if err := os.WriteFile(frozen, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		publication := &atomicOutputPublication{}
		err := publication.rejectFrozenAncestor(frozen)
		if err == nil || !strings.Contains(err.Error(), "open frozen fixture directory") {
			t.Fatalf("error = %v, want frozen directory open failure", err)
		}
	})

	t.Run("ancestor walk rejects closed parent descriptor", func(t *testing.T) {
		parent, err := openDirectoryNoFollow(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err = parent.Close(); err != nil {
			t.Fatal(err)
		}
		publication := &atomicOutputPublication{parent: parent}
		err = publication.rejectFrozenAncestor(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "duplicate output parent descriptor") {
			t.Fatalf("error = %v, want descriptor duplication failure", err)
		}
	})

	t.Run("ancestor walk rejects non-directory parent descriptor", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
			t.Fatal(err)
		}
		parent, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer parent.Close()
		publication := &atomicOutputPublication{parent: parent}
		err = publication.rejectFrozenAncestor(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "open output ancestor") {
			t.Fatalf("error = %v, want ancestor open failure", err)
		}
	})

	t.Run("cleanup without recorded identity removes nothing", func(t *testing.T) {
		publication := &atomicOutputPublication{
			stageName: "unknown-stage",
			published: true,
		}
		if err := publication.removeOwnedStage(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cleanup preserves unverified empty stage", func(t *testing.T) {
		parentPath := t.TempDir()
		stageName := "stage"
		stagePath := filepath.Join(parentPath, stageName)
		if err := os.Mkdir(stagePath, 0o700); err != nil {
			t.Fatal(err)
		}
		parent, err := openDirectoryNoFollow(parentPath)
		if err != nil {
			t.Fatal(err)
		}
		publication := &atomicOutputPublication{
			parent:    parent,
			stageName: stageName,
		}
		if err = publication.removeOwnedStage(); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Lstat(stagePath); err != nil {
			t.Fatalf("unverified stage was removed: %v", err)
		}
		if err = os.Remove(stagePath); err != nil {
			t.Fatal(err)
		}
		if err = parent.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cleanup preserves unverified non-empty stage", func(t *testing.T) {
		parentPath := t.TempDir()
		stageName := "replacement"
		stagePath := filepath.Join(parentPath, stageName)
		if err := os.Mkdir(stagePath, 0o700); err != nil {
			t.Fatal(err)
		}
		sentinel := filepath.Join(stagePath, "unowned")
		if err := os.WriteFile(sentinel, []byte("replacement"), 0o644); err != nil {
			t.Fatal(err)
		}
		parent, err := openDirectoryNoFollow(parentPath)
		if err != nil {
			t.Fatal(err)
		}
		publication := &atomicOutputPublication{
			parent:    parent,
			stageName: stageName,
		}
		if err = publication.removeOwnedStage(); err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, sentinel, "replacement")
		if err = parent.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPathResolutionErrors(t *testing.T) {
	invalidPath := string([]byte{'b', 'a', 'd', 0, 'p', 'a', 't', 'h'})

	t.Run("resolve existing components", func(t *testing.T) {
		_, err := resolveExistingPathComponents(invalidPath)
		if err == nil {
			t.Fatal("invalid path resolved successfully")
		}
	})

	t.Run("resolve frozen directory", func(t *testing.T) {
		_, err := pathWithinResolvedDir(invalidPath, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "resolve frozen fixture directory:") {
			t.Fatalf("error = %v, want frozen path resolution error", err)
		}
	})

	t.Run("resolve output directory", func(t *testing.T) {
		_, err := pathWithinResolvedDir(t.TempDir(), invalidPath)
		if err == nil || !strings.Contains(err.Error(), "resolve output directory symlinks:") {
			t.Fatalf("error = %v, want output path resolution error", err)
		}
	})

	t.Run("validate output destination", func(t *testing.T) {
		err := validateOutputDestination(invalidPath, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "resolve frozen fixture directory:") {
			t.Fatalf("error = %v, want destination validation error", err)
		}
	})
}

func TestRemoveOwnedCandidate(t *testing.T) {
	t.Run("preserves unverified empty candidate", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		actualCandidateIdentity := publication.candidateIdentity
		publication.candidateIdentity = nil

		if err = publication.removeOwnedCandidate(); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Lstat(filepath.Join(
			parent, publication.stageName, stagedOutputName,
		)); err != nil {
			t.Fatalf("unverified candidate was removed: %v", err)
		}
		publication.candidateIdentity = actualCandidateIdentity
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})

	t.Run("preserves unverified non-empty candidate", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		candidatePath := filepath.Join(parent, publication.stageName, stagedOutputName)
		sentinel := filepath.Join(candidatePath, "unowned")
		if err = os.WriteFile(sentinel, []byte("replacement"), 0o644); err != nil {
			t.Fatal(err)
		}
		publication.candidateIdentity = nil

		if err = publication.removeOwnedCandidate(); err != nil {
			t.Fatal(err)
		}
		assertFileContent(t, sentinel, "replacement")
		if err = os.Remove(sentinel); err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(candidatePath); err != nil {
			t.Fatal(err)
		}
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})

	t.Run("reopens and removes owned candidate", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		if err = publication.writeFile(
			generatedFixtureFile{name: "fixture", data: []byte("checkpoint")},
			regenerationHooks{},
		); err != nil {
			t.Fatal(err)
		}
		if err = publication.candidate.Close(); err != nil {
			t.Fatal(err)
		}
		publication.candidate = nil

		if err = publication.removeOwnedCandidate(); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(
			parent, publication.stageName, stagedOutputName,
		)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("candidate remains after cleanup: %v", err)
		}
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})

	t.Run("accepts already removed candidate", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		if err = publication.candidate.Close(); err != nil {
			t.Fatal(err)
		}
		publication.candidate = nil
		if err = os.Remove(filepath.Join(
			parent, publication.stageName, stagedOutputName,
		)); err != nil {
			t.Fatal(err)
		}

		if err = publication.removeOwnedCandidate(); err != nil {
			t.Fatal(err)
		}
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})

	t.Run("rejects candidate identity mismatch", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		actualCandidateIdentity := publication.candidateIdentity
		publication.candidateIdentity = publication.stageIdentity

		err = publication.removeOwnedCandidate()
		wantErr := "staged output directory identity changed during cleanup"
		requireEqualError(t, err, wantErr)
		publication.candidateIdentity = actualCandidateIdentity
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})

	t.Run("does not remove unexpected candidate content", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		unexpected := filepath.Join(
			parent, publication.stageName, stagedOutputName, "unowned")
		if err = os.WriteFile(unexpected, []byte("unowned"), 0o644); err != nil {
			t.Fatal(err)
		}

		err = publication.removeOwnedCandidate()
		if err == nil || !strings.Contains(err.Error(), "remove staged output directory") {
			t.Fatalf("error = %v, want non-empty candidate cleanup failure", err)
		}
		assertFileContent(t, unexpected, "unowned")

		if err = os.Remove(unexpected); err != nil {
			t.Fatal(err)
		}
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})

	t.Run("does not open replacement candidate entry", func(t *testing.T) {
		parent := t.TempDir()
		publication, err := reserveAtomicOutput("", filepath.Join(parent, "output"))
		if err != nil {
			t.Fatal(err)
		}
		if err = publication.candidate.Close(); err != nil {
			t.Fatal(err)
		}
		publication.candidate = nil
		candidatePath := filepath.Join(parent, publication.stageName, stagedOutputName)
		if err = os.Remove(candidatePath); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(candidatePath, []byte("unowned"), 0o644); err != nil {
			t.Fatal(err)
		}

		err = publication.removeOwnedCandidate()
		if err == nil || !strings.Contains(err.Error(), "inspect staged output directory for cleanup") {
			t.Fatalf("error = %v, want candidate identity failure", err)
		}
		assertFileContent(t, candidatePath, "unowned")

		if err = os.Remove(candidatePath); err != nil {
			t.Fatal(err)
		}
		if err = publication.closeAndCleanup(); err != nil {
			t.Fatal(err)
		}
		assertNoStagingDirectories(t, parent)
	})
}

func TestDirectoryIdentityHelpers(t *testing.T) {
	parentPath := t.TempDir()
	parent, err := openDirectoryNoFollow(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stageName := "stage"
	if err = os.Mkdir(filepath.Join(parentPath, stageName), 0o755); err != nil {
		t.Fatal(err)
	}

	identity, err := directoryEntryIdentity(int(parent.Fd()), stageName)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := openDirectoryAt(int(parent.Fd()), stageName)
	if err != nil {
		t.Fatal(err)
	}
	openedIdentity, err := directoryDescriptorIdentity(stage)
	if err != nil {
		t.Fatal(err)
	}
	if err = stage.Close(); err != nil {
		t.Fatal(err)
	}
	if identity != openedIdentity {
		t.Fatalf("entry identity = %#v, descriptor identity = %#v",
			identity, openedIdentity)
	}
	matches, err := directoryEntryMatchesIdentity(int(parent.Fd()), stageName, identity)
	if err != nil || !matches {
		t.Fatalf("owned entry match = %v, %v; want true, nil", matches, err)
	}

	if err = os.Rename(filepath.Join(parentPath, stageName),
		filepath.Join(parentPath, "owned-stage")); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(parentPath, stageName), 0o755); err != nil {
		t.Fatal(err)
	}
	matches, err = directoryEntryMatchesIdentity(int(parent.Fd()), stageName, identity)
	if err != nil || matches {
		t.Fatalf("replacement entry match = %v, %v; want false, nil", matches, err)
	}
	matches, err = directoryEntryMatchesIdentity(int(parent.Fd()), "missing", identity)
	if err != nil || matches {
		t.Fatalf("missing entry match = %v, %v; want false, nil", matches, err)
	}

	fileName := "file"
	if err = os.WriteFile(filepath.Join(parentPath, fileName), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = directoryEntryIdentity(int(parent.Fd()), fileName)
	requireEqualError(t, err, "entry is not a directory")
	matches, err = directoryEntryMatchesIdentity(int(parent.Fd()), fileName, identity)
	if err == nil || matches {
		t.Fatalf("non-directory entry match = %v, %v; want false, error", matches, err)
	}

	closedParent, err := openDirectoryNoFollow(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = closedParent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = directoryEntryIdentity(int(closedParent.Fd()), "stage"); err == nil {
		t.Fatal("identity lookup with closed parent succeeded")
	}
	if _, err = directoryDescriptorIdentity(closedParent); err == nil {
		t.Fatal("descriptor identity lookup with closed descriptor succeeded")
	}
	if _, err = directoryEntryMatchesIdentity(
		int(closedParent.Fd()), "stage", identity); err == nil {
		t.Fatal("entry match with closed parent succeeded")
	}
}

func TestPublishOutputRejectsCaseInsensitiveAlias(t *testing.T) {
	parent := t.TempDir()
	if !filesystemIsCaseInsensitive(t, parent) {
		t.Skip("filesystem is case-sensitive")
	}
	existing := filepath.Join(parent, "Output")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(existing, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unowned"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "output")

	err := publishOutput("", destination, nil, regenerationHooks{})
	wantErr := "output directory already exists: " + destination
	requireEqualError(t, err, wantErr)
	assertFileContent(t, sentinel, "unowned")
	assertNoStagingDirectories(t, parent)
}

func newSuccessfulFakeGit(t *testing.T) *fakeGit {
	t.Helper()
	return &fakeGit{
		repoRoot: t.TempDir(),
		parent:   producerCommit,
		materialize: func(worktreeDir string) error {
			dir := filepath.Join(worktreeDir, fixtureRelativeDir)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			raw := []byte("historical checkpoint")
			compressed, err := gzipBytes(raw)
			if err != nil {
				return err
			}
			file := "parallel_6.bin.gz"
			if err = os.WriteFile(filepath.Join(dir, file), compressed, 0o644); err != nil {
				return err
			}
			sum := sha256.Sum256(raw)
			data, err := json.MarshalIndent(&historicalManifest{
				ProducerCommit: producerCommit,
				Fixtures: []fixture{{
					Name:   "parallel_6",
					File:   file,
					SHA256: hex.EncodeToString(sum[:]),
				}},
			}, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "manifest.json"), append(data, '\n'), 0o644)
		},
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

func assertAnnotatedOutput(t *testing.T, outputDir string) {
	t.Helper()
	raw, err := readGzipFile(filepath.Join(outputDir, "parallel_6.bin.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "historical checkpoint" {
		t.Fatalf("copied fixture = %q", raw)
	}
	data, err := os.ReadFile(filepath.Join(outputDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got manifest
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ProducerCommit != producerCommit || got.GeneratorCommit != generatorCommit ||
		len(got.Fixtures) != 1 || !got.Fixtures[0].ImplicitResume {
		t.Fatalf("copied manifest = %#v", got)
	}
}

func assertDirectoriesEqual(t *testing.T, wantDir, gotDir string) {
	t.Helper()
	want := readDirectoryFiles(t, wantDir)
	got := readDirectoryFiles(t, gotDir)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory contents differ:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func assertDirectoryFilesEqual(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	got := readDirectoryFiles(t, dir)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory contents changed:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func assertFailedPublication(t *testing.T, outputDir, stdout string) {
	t.Helper()
	if stdout != "" {
		t.Fatalf("stdout = %q, want no success output", stdout)
	}
	assertNoOutputArtifacts(t, outputDir)
}

func assertNoOutputArtifacts(t *testing.T, outputDir string) {
	t.Helper()
	if _, err := os.Lstat(outputDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output exists after failure: %v", err)
	}
	assertNoStagingDirectories(t, filepath.Dir(outputDir))
}

func assertNoStagingDirectories(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingDirectoryPrefix) {
			t.Fatalf("staging artifact remains after failure: %s", entry.Name())
		}
	}
}

func findOnlyStagingDirectory(t *testing.T, parent string) string {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var found string
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), stagingDirectoryPrefix) {
			continue
		}
		if found != "" {
			t.Fatalf("multiple staging directories found: %s and %s", found, entry.Name())
		}
		found = filepath.Join(parent, entry.Name())
	}
	if found == "" {
		t.Fatal("staging directory not found")
	}
	return found
}

func assertDirectoryEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory %s contains unowned entries: %v", dir, entries)
	}
}

func readDirectoryFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[relative], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func containsCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

func filesystemIsCaseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	probe, err := os.CreateTemp(dir, "eino-case-probe-a")
	if err != nil {
		t.Fatal(err)
	}
	probePath := probe.Name()
	if err = probe.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(probePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove case-sensitivity probe: %v", err)
		}
	})
	probeInfo, err := os.Stat(probePath)
	if err != nil {
		t.Fatal(err)
	}
	variantInfo, err := os.Stat(filepath.Join(filepath.Dir(probePath),
		strings.ToUpper(filepath.Base(probePath))))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(probeInfo, variantInfo)
}

func writeGzipFile(t *testing.T, path string, raw []byte) []byte {
	t.Helper()
	compressed, err := gzipBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, compressed, 0o644); err != nil {
		t.Fatal(err)
	}
	return compressed
}

func gzipBytes(raw []byte) ([]byte, error) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func readGzipFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
