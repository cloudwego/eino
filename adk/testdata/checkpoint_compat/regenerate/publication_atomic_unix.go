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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const stagedOutputName = "candidate"

type atomicOutputPublication struct {
	outputPath        string
	parentPath        string
	outputName        string
	parent            *os.File
	parentInfo        os.FileInfo
	stageName         string
	stageIdentity     *directoryIdentity
	stage             *os.File
	candidate         *os.File
	candidateIdentity *directoryIdentity
	candidateInfo     os.FileInfo
	fileNames         []string
	published         bool
	hooks             regenerationHooks
}

func atomicPublicationSupported() error {
	return nil
}

func publishOutput(frozenDir, outputDir string, files []generatedFixtureFile,
	hooks regenerationHooks) (retErr error) {
	if err := validateGeneratedFixtureFiles(files); err != nil {
		return err
	}
	if hooks.beforeOutputReserved != nil {
		hooks.beforeOutputReserved()
	}
	publication, err := reserveAtomicOutputWithHooks(frozenDir, outputDir, hooks)
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := publication.closeAndCleanup(); cleanupErr != nil {
			if retErr == nil {
				retErr = cleanupErr
			} else {
				retErr = fmt.Errorf("%w; cleanup output staging: %v", retErr, cleanupErr)
			}
		}
	}()

	if hooks.afterOutputReserved != nil {
		hooks.afterOutputReserved()
	}
	for i := range files {
		if err = publication.writeFile(files[i], hooks); err != nil {
			return err
		}
	}
	if err = publication.sealCandidate(); err != nil {
		return err
	}
	if err = publication.validateParentIdentity(); err != nil {
		return err
	}

	if hooks.beforeOutputPublished != nil {
		hooks.beforeOutputPublished()
	}
	if hooks.publishOutput != nil {
		if err = hooks.publishOutput(); err != nil {
			return err
		}
	}
	if err = publication.validateParentIdentity(); err != nil {
		return err
	}
	if err = publication.validateCandidateIdentity(); err != nil {
		return err
	}
	if err = publication.validateStageIdentity(); err != nil {
		return err
	}
	if err = renameDirectoryNoReplace(
		int(publication.stage.Fd()), stagedOutputName,
		int(publication.parent.Fd()), publication.outputName,
	); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return outputExistsError{path: outputDir}
		}
		return fmt.Errorf("publish output directory: %w", err)
	}
	publication.published = true
	return nil
}

func reserveAtomicOutput(frozenDir, outputDir string) (
	_ *atomicOutputPublication, retErr error,
) {
	return reserveAtomicOutputWithHooks(frozenDir, outputDir, regenerationHooks{})
}

func reserveAtomicOutputWithHooks(frozenDir, outputDir string, hooks regenerationHooks) (
	_ *atomicOutputPublication, retErr error,
) {
	parentPath := filepath.Dir(outputDir)
	outputName := filepath.Base(outputDir)
	if outputName == "." || outputName == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid output directory: %s", outputDir)
	}

	parent, err := openDirectoryNoFollow(parentPath)
	if err != nil {
		return nil, fmt.Errorf("open output parent directory: %w", err)
	}
	parentInfo, err := parent.Stat()
	if err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("inspect output parent directory: %w", err)
	}
	publication := &atomicOutputPublication{
		outputPath: outputDir,
		parentPath: parentPath,
		outputName: outputName,
		parent:     parent,
		parentInfo: parentInfo,
		hooks:      hooks,
	}
	defer func() {
		if retErr != nil {
			if cleanupErr := publication.closeAndCleanup(); cleanupErr != nil {
				retErr = fmt.Errorf("%w; cleanup output staging: %v", retErr, cleanupErr)
			}
		}
	}()

	if err = publication.rejectFrozenAncestor(frozenDir); err != nil {
		return nil, err
	}
	if err = ensureNameAbsent(int(parent.Fd()), outputName); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, outputExistsError{path: outputDir}
		}
		return nil, fmt.Errorf("inspect output directory: %w", err)
	}
	if err = publication.createStage(); err != nil {
		return nil, err
	}
	return publication, nil
}

func (p *atomicOutputPublication) createStage() error {
	var randomBytes [12]byte
	// Ownership starts at the first successful entry-identity capture. POSIX
	// cannot portably create a directory and return its descriptor, so the
	// local-tool threat model excludes same-UID mutation before that capture.
	for attempts := 0; attempts < 100; attempts++ {
		if _, err := rand.Read(randomBytes[:]); err != nil {
			return fmt.Errorf("generate output staging name: %w", err)
		}
		stageName := stagingDirectoryPrefix + hex.EncodeToString(randomBytes[:])
		if err := unix.Mkdirat(int(p.parent.Fd()), stageName, 0o700); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return fmt.Errorf("reserve output staging directory: %w", err)
		}
		p.stageName = stageName
		createdIdentity, identityErr := directoryEntryIdentity(
			int(p.parent.Fd()), p.stageName)
		if identityErr != nil {
			return fmt.Errorf("inspect created output staging directory: %w", identityErr)
		}
		p.stageIdentity = &createdIdentity

		var stage *os.File
		var err error
		if p.hooks.openOutputStage != nil {
			stage, err = p.hooks.openOutputStage(int(p.parent.Fd()), p.stageName)
		} else {
			stage, err = openDirectoryAt(int(p.parent.Fd()), p.stageName)
		}
		if err != nil {
			return fmt.Errorf("open output staging directory: %w", err)
		}
		var openedIdentity directoryIdentity
		if p.hooks.inspectOutputStageDescriptor != nil {
			openedIdentity, identityErr = p.hooks.inspectOutputStageDescriptor(stage)
		} else {
			openedIdentity, identityErr = directoryDescriptorIdentity(stage)
		}
		if identityErr != nil {
			_ = stage.Close()
			return fmt.Errorf("inspect opened output staging directory: %w", identityErr)
		}
		if openedIdentity != createdIdentity {
			_ = stage.Close()
			return errors.New("output staging directory identity changed after creation")
		}
		p.stage = stage
		if p.hooks.afterOutputStageOpened != nil {
			if err = p.hooks.afterOutputStageOpened(
				int(p.parent.Fd()), p.stageName); err != nil {
				return fmt.Errorf("open output staging directory: %w", err)
			}
		}
		var identity directoryIdentity
		if p.hooks.inspectOutputStageEntry != nil {
			identity, err = p.hooks.inspectOutputStageEntry(
				int(p.parent.Fd()), p.stageName)
		} else {
			identity, err = directoryEntryIdentity(int(p.parent.Fd()), p.stageName)
		}
		if err != nil {
			return fmt.Errorf("inspect created output staging directory: %w", err)
		}
		if identity != createdIdentity {
			return errors.New("output staging directory identity changed after creation")
		}
		if p.hooks.inspectOutputStage != nil {
			_, err = p.hooks.inspectOutputStage(p.stage)
		} else {
			_, err = p.stage.Stat()
		}
		if err != nil {
			return fmt.Errorf("inspect output staging directory: %w", err)
		}
		// Candidate ownership likewise begins only after its first identity capture.
		if p.hooks.makeOutputCandidate != nil {
			err = p.hooks.makeOutputCandidate(int(p.stage.Fd()), stagedOutputName)
		} else {
			err = unix.Mkdirat(int(p.stage.Fd()), stagedOutputName, 0o700)
		}
		if err != nil {
			return fmt.Errorf("reserve staged output directory: %w", err)
		}
		createdCandidateIdentity, identityErr := directoryEntryIdentity(
			int(p.stage.Fd()), stagedOutputName)
		if identityErr != nil {
			return fmt.Errorf("inspect created staged output directory: %w", identityErr)
		}
		p.candidateIdentity = &createdCandidateIdentity

		var candidate *os.File
		if p.hooks.openOutputCandidate != nil {
			candidate, err = p.hooks.openOutputCandidate(
				int(p.stage.Fd()), stagedOutputName)
		} else {
			candidate, err = openDirectoryAt(int(p.stage.Fd()), stagedOutputName)
		}
		if err != nil {
			return fmt.Errorf("open staged output directory: %w", err)
		}
		var openedCandidateIdentity directoryIdentity
		if p.hooks.inspectOutputCandidateDescriptor != nil {
			openedCandidateIdentity, identityErr =
				p.hooks.inspectOutputCandidateDescriptor(candidate)
		} else {
			openedCandidateIdentity, identityErr = directoryDescriptorIdentity(candidate)
		}
		if identityErr != nil {
			_ = candidate.Close()
			return fmt.Errorf("inspect opened staged output directory: %w", identityErr)
		}
		if openedCandidateIdentity != createdCandidateIdentity {
			_ = candidate.Close()
			return errors.New("staged output directory identity changed after creation")
		}
		p.candidate = candidate
		if p.hooks.afterOutputCandidateOpened != nil {
			if err = p.hooks.afterOutputCandidateOpened(
				int(p.stage.Fd()), stagedOutputName); err != nil {
				return fmt.Errorf("open staged output directory: %w", err)
			}
		}
		var candidateIdentity directoryIdentity
		if p.hooks.inspectOutputCandidateEntry != nil {
			candidateIdentity, err = p.hooks.inspectOutputCandidateEntry(
				int(p.stage.Fd()), stagedOutputName)
		} else {
			candidateIdentity, err = directoryEntryIdentity(
				int(p.stage.Fd()), stagedOutputName)
		}
		if err != nil {
			return fmt.Errorf("inspect created staged output directory: %w", err)
		}
		if candidateIdentity != createdCandidateIdentity {
			return errors.New("staged output directory identity changed after creation")
		}
		if p.hooks.inspectOutputCandidate != nil {
			p.candidateInfo, err = p.hooks.inspectOutputCandidate(p.candidate)
		} else {
			p.candidateInfo, err = p.candidate.Stat()
		}
		if err != nil {
			return fmt.Errorf("inspect staged output directory: %w", err)
		}
		return nil
	}
	return errors.New("reserve output staging directory: exhausted name attempts")
}

func (p *atomicOutputPublication) writeFile(generated generatedFixtureFile,
	hooks regenerationHooks) error {
	fd, err := unix.Openat(int(p.candidate.Fd()), generated.name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("reserve output fixture %s: %w", generated.name, err)
	}
	p.fileNames = append(p.fileNames, generated.name)
	file := os.NewFile(uintptr(fd), generated.name)
	if hooks.writeOutputFile != nil {
		err = hooks.writeOutputFile(generated.name, file, generated.data)
	} else {
		_, err = io.Copy(file, bytes.NewReader(generated.data))
	}
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("copy output fixture %s: %w", generated.name, err)
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync output fixture %s: %w", generated.name, err)
	}
	if hooks.closeOutputFile != nil {
		err = hooks.closeOutputFile(generated.name, file)
	} else {
		err = file.Close()
	}
	if err != nil {
		return fmt.Errorf("close output fixture %s: %w", generated.name, err)
	}
	return nil
}

func (p *atomicOutputPublication) sealCandidate() error {
	if p.candidate == nil {
		return nil
	}
	if err := unix.Fchmod(int(p.candidate.Fd()), 0o755); err != nil {
		return fmt.Errorf("set output directory permissions: %w", err)
	}
	if err := p.candidate.Sync(); err != nil {
		return fmt.Errorf("sync output staging directory: %w", err)
	}
	return nil
}

func (p *atomicOutputPublication) validateParentIdentity() error {
	current, err := openDirectoryNoFollow(p.parentPath)
	if err != nil {
		return fmt.Errorf("output directory identity changed after reservation: %s", p.outputPath)
	}
	info, err := current.Stat()
	if err != nil || !os.SameFile(p.parentInfo, info) {
		_ = current.Close()
		return fmt.Errorf("output directory identity changed after reservation: %s", p.outputPath)
	}
	if err = current.Close(); err != nil {
		return fmt.Errorf("close output parent identity descriptor: %w", err)
	}
	return nil
}

func (p *atomicOutputPublication) validateCandidateIdentity() error {
	if p.candidate == nil || p.candidateIdentity == nil || p.candidateInfo == nil {
		return errors.New("output staging candidate is not reserved")
	}
	identity, err := directoryDescriptorIdentity(p.candidate)
	if err != nil || identity != *p.candidateIdentity {
		return errors.New("output staging candidate identity changed before publication")
	}
	info, err := p.candidate.Stat()
	if err != nil || !os.SameFile(p.candidateInfo, info) {
		return errors.New("output staging candidate identity changed before publication")
	}
	matches, err := directoryEntryMatchesIdentity(
		int(p.stage.Fd()), stagedOutputName, *p.candidateIdentity)
	if err != nil {
		return fmt.Errorf("inspect output staging candidate: %w", err)
	}
	if !matches {
		return errors.New("output staging candidate identity changed before publication")
	}
	return nil
}

func (p *atomicOutputPublication) validateStageIdentity() error {
	if p.stage == nil || p.stageIdentity == nil {
		return errors.New("output staging directory is not reserved")
	}
	identity, err := directoryDescriptorIdentity(p.stage)
	if err != nil || identity != *p.stageIdentity {
		return errors.New("output staging directory identity changed before publication")
	}
	matches, err := directoryEntryMatchesIdentity(
		int(p.parent.Fd()), p.stageName, *p.stageIdentity)
	if err != nil {
		return fmt.Errorf("inspect output staging directory before publication: %w", err)
	}
	if !matches {
		return errors.New("output staging directory identity changed before publication")
	}
	return nil
}

func (p *atomicOutputPublication) rejectFrozenAncestor(frozenDir string) error {
	if frozenDir == "" {
		return nil
	}
	frozen, err := openDirectoryNoFollow(frozenDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open frozen fixture directory: %w", err)
	}
	frozenInfo, err := frozen.Stat()
	if err != nil {
		_ = frozen.Close()
		return fmt.Errorf("inspect frozen fixture directory: %w", err)
	}
	if err = frozen.Close(); err != nil {
		return fmt.Errorf("close frozen fixture directory: %w", err)
	}

	currentFD, err := unix.Dup(int(p.parent.Fd()))
	if err != nil {
		return fmt.Errorf("duplicate output parent descriptor: %w", err)
	}
	current := os.NewFile(uintptr(currentFD), p.parentPath)
	for {
		currentInfo, statErr := current.Stat()
		if statErr != nil {
			_ = current.Close()
			return fmt.Errorf("inspect output ancestor: %w", statErr)
		}
		if os.SameFile(frozenInfo, currentInfo) {
			_ = current.Close()
			return fmt.Errorf("refusing to overwrite frozen fixtures at %s", frozenDir)
		}
		parentFD, openErr := unix.Openat(int(current.Fd()), "..",
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			_ = current.Close()
			return fmt.Errorf("open output ancestor: %w", openErr)
		}
		parent := os.NewFile(uintptr(parentFD), "..")
		parentInfo, statErr := parent.Stat()
		if statErr != nil {
			_ = parent.Close()
			_ = current.Close()
			return fmt.Errorf("inspect output ancestor: %w", statErr)
		}
		if os.SameFile(currentInfo, parentInfo) {
			if closeErr := current.Close(); closeErr != nil {
				_ = parent.Close()
				return fmt.Errorf("close output ancestor: %w", closeErr)
			}
			if closeErr := parent.Close(); closeErr != nil {
				return fmt.Errorf("close output ancestor: %w", closeErr)
			}
			return nil
		}
		if closeErr := current.Close(); closeErr != nil {
			_ = parent.Close()
			return fmt.Errorf("close output ancestor: %w", closeErr)
		}
		current = parent
	}
}

func (p *atomicOutputPublication) closeAndCleanup() error {
	var firstErr error
	if p.published && p.candidate != nil {
		if err := p.candidate.Close(); err != nil {
			firstErr = fmt.Errorf("close published output directory: %w", err)
		}
		p.candidate = nil
	}
	if p.stageName != "" {
		if err := p.removeOwnedStage(); firstErr == nil && err != nil {
			firstErr = err
		}
	}
	if p.parent != nil {
		if err := p.parent.Close(); firstErr == nil && err != nil && !p.published {
			firstErr = fmt.Errorf("close output parent directory: %w", err)
		}
		p.parent = nil
	}
	return firstErr
}

func (p *atomicOutputPublication) removeOwnedStage() error {
	if p.stage != nil {
		if err := p.removeOwnedCandidate(); err != nil {
			return err
		}
	}
	if p.stage != nil {
		if err := p.stage.Close(); err != nil {
			p.stage = nil
			return fmt.Errorf("close output staging directory: %w", err)
		}
		p.stage = nil
	}
	if p.stageIdentity == nil {
		// Before ownership is captured, the name may refer to another object.
		return nil
	}
	matches, err := directoryEntryMatchesIdentity(
		int(p.parent.Fd()), p.stageName, *p.stageIdentity)
	if err != nil {
		return fmt.Errorf("inspect output staging directory for cleanup: %w", err)
	}
	if !matches {
		return nil
	}
	if err = unix.Unlinkat(int(p.parent.Fd()), p.stageName, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove output staging directory: %w", err)
	}
	p.stageName = ""
	p.stageIdentity = nil
	return nil
}

func (p *atomicOutputPublication) removeOwnedCandidate() error {
	if p.published {
		return nil
	}
	candidate := p.candidate
	if p.candidateIdentity == nil {
		if candidate != nil {
			if err := candidate.Close(); err != nil {
				p.candidate = nil
				return fmt.Errorf("close unverified staged output directory: %w", err)
			}
			p.candidate = nil
		}
		// Before ownership is captured, the name may refer to another object.
		return nil
	}
	if candidate == nil {
		matches, err := directoryEntryMatchesIdentity(
			int(p.stage.Fd()), stagedOutputName, *p.candidateIdentity)
		if err != nil {
			return fmt.Errorf("inspect staged output directory for cleanup: %w", err)
		}
		if !matches {
			return nil
		}
		reopened, err := openDirectoryAt(int(p.stage.Fd()), stagedOutputName)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("open staged output directory for cleanup: %w", err)
		}
		candidate = reopened
	}
	identity, err := directoryDescriptorIdentity(candidate)
	if err != nil {
		_ = candidate.Close()
		p.candidate = nil
		return fmt.Errorf("inspect staged output directory for cleanup: %w", err)
	}
	if identity != *p.candidateIdentity {
		_ = candidate.Close()
		p.candidate = nil
		return errors.New("staged output directory identity changed during cleanup")
	}
	for i := len(p.fileNames) - 1; i >= 0; i-- {
		if err = unix.Unlinkat(int(candidate.Fd()), p.fileNames[i], 0); err != nil &&
			!errors.Is(err, unix.ENOENT) {
			_ = candidate.Close()
			p.candidate = nil
			return fmt.Errorf("remove staged output fixture %s: %w", p.fileNames[i], err)
		}
	}
	if err = candidate.Close(); err != nil {
		p.candidate = nil
		return fmt.Errorf("close staged output directory during cleanup: %w", err)
	}
	p.candidate = nil

	matches, err := directoryEntryMatchesIdentity(
		int(p.stage.Fd()), stagedOutputName, *p.candidateIdentity)
	if err != nil {
		return fmt.Errorf("inspect staged output directory for cleanup: %w", err)
	}
	if !matches {
		return nil
	}
	if err = unix.Unlinkat(int(p.stage.Fd()), stagedOutputName, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove staged output directory: %w", err)
	}
	p.candidateIdentity = nil
	p.candidateInfo = nil
	return nil
}

func openDirectoryNoFollow(path string) (*os.File, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(resolved) {
		return nil, fmt.Errorf("directory path is not absolute: %s", resolved)
	}

	fd, err := unix.Open(string(filepath.Separator),
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(filepath.Separator))
	parts := strings.Split(strings.TrimPrefix(resolved, string(filepath.Separator)),
		string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		nextFD, openErr := unix.Openat(int(current.Fd()), part,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			_ = current.Close()
			return nil, openErr
		}
		next := os.NewFile(uintptr(nextFD), part)
		if closeErr := current.Close(); closeErr != nil {
			_ = next.Close()
			return nil, closeErr
		}
		current = next
	}
	return current, nil
}

func openDirectoryAt(parentFD int, name string) (*os.File, error) {
	fd, err := unix.Openat(parentFD, name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func directoryEntryIdentity(parentFD int, name string) (directoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return directoryIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return directoryIdentity{}, errors.New("entry is not a directory")
	}
	return directoryIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func directoryDescriptorIdentity(directory *os.File) (directoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil {
		return directoryIdentity{}, err
	}
	return directoryIdentity{
		device: uint64(stat.Dev),
		inode:  uint64(stat.Ino),
	}, nil
}

func directoryEntryMatchesIdentity(parentFD int, name string,
	want directoryIdentity) (bool, error) {
	got, err := directoryEntryIdentity(parentFD, name)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) ||
		errors.Is(err, unix.ELOOP) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == want, nil
}

func ensureNameAbsent(parentFD int, name string) error {
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return os.ErrExist
	}
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}
