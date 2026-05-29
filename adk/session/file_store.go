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

package session

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
)

// FileStore is a process-local, file-backed implementation of adk.SessionStore.
// Each session is stored as one event log file under the configured directory:
//
//	<root>/<url.PathEscape(sessionID)>.evlog
//
// Each line is formatted as: <EventID>\t<Kind>\t<Data>\n
// where Data is the raw serialized bytes written directly to the line.
//
// IMPORTANT: FileStore requires that SessionEventPayload.Data does NOT contain
// raw newline (\n) or carriage-return (\r) characters, because these would
// corrupt the line-oriented file format. The default HumanReadableSerializer
// (compact JSON) satisfies this constraint. Serializers that may emit \n or \r
// in their output (e.g. GobSerializer, raw protobuf) are NOT compatible with
// FileStore — use InMemoryStore or a custom store implementation instead.
// AppendEvents will return an error if Data contains \n or \r.
//
// FileStore does not implement CheckPointStore; runner checkpoints should use
// a dedicated checkpoint store.
//
// FileStore synchronizes access within the current process. It does not provide
// cross-process write safety.
type FileStore struct {
	dir     string
	mu      sync.Mutex
	indexes map[string]*fileSessionIndex
}

type fileEvent struct {
	payload adk.SessionEventPayload
}

type fileSessionIndex struct {
	size          int64
	modTime       time.Time
	offsets       []int64
	eventIDToLine map[string]int
}

// NewFileStore creates a file-backed SessionStore rooted at dir.
func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, errorsNewEmptySessionStoreDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir, indexes: make(map[string]*fileSessionIndex)}, nil
}

func errorsNewEmptySessionStoreDir() error {
	return fmt.Errorf("adk/session: file store dir is empty")
}

func errorsNewEmptySessionID() error {
	return fmt.Errorf("adk/session: sessionID is empty")
}

// AppendEvents appends events to the session's event log.
//
// Each SessionEventPayload.EventID MUST be non-empty. Duplicate event IDs are
// skipped with first-write-wins semantics, including duplicates within the
// same batch.
func (s *FileStore) AppendEvents(_ context.Context, sessionID string, events []adk.SessionEventPayload) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.sessionPath(sessionID)
	if err != nil {
		return err
	}

	// Validate incoming events and dedup within batch.
	seen := make(map[string]struct{}, len(events))
	pending := make([]adk.SessionEventPayload, 0, len(events))
	for _, e := range events {
		if e.EventID == "" {
			return adk.ErrInvalidEventID
		}
		if _, dup := seen[e.EventID]; dup {
			continue
		}
		seen[e.EventID] = struct{}{}
		pending = append(pending, e)
	}
	if len(pending) == 0 {
		return nil
	}

	idx, err := s.ensureIndexLocked(path)
	if err != nil {
		return err
	}

	var out *os.File
	for _, event := range pending {
		if _, dup := idx.eventIDToLine[event.EventID]; dup {
			continue
		}
		if bytes.ContainsAny(event.Data, "\r\n") {
			return fmt.Errorf("adk/session: FileStore requires Data without raw CR/LF; use a line-safe serializer (e.g. HumanReadableSerializer)")
		}
		if out == nil {
			out, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return err
			}
			defer out.Close()
		}
		line := fmt.Sprintf("%s\t%s\t%s\n", event.EventID, event.Kind, event.Data)
		n, err := out.WriteString(line)
		if err != nil {
			return err
		}
		idx.eventIDToLine[event.EventID] = len(idx.offsets)
		idx.offsets = append(idx.offsets, idx.size)
		idx.size += int64(n)
	}
	if out != nil {
		info, err := out.Stat()
		if err != nil {
			return err
		}
		idx.size = info.Size()
		idx.modTime = info.ModTime()
	}
	return nil
}

// LoadEvents loads events with pagination and direction support.
func (s *FileStore) LoadEvents(_ context.Context, sessionID string, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.sessionPath(sessionID)
	if err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &adk.LoadEventsRequest{}
	}
	idx, err := s.ensureIndexLocked(path)
	if err != nil {
		return nil, err
	}
	if opts.Reverse {
		return s.loadFileEventsReverseLocked(path, idx, opts)
	}
	return s.loadFileEventsForwardLocked(path, idx, opts)
}

func (s *FileStore) sessionPath(sessionID string) (string, error) {
	if sessionID == "" {
		return "", errorsNewEmptySessionID()
	}
	return filepath.Join(s.dir, url.PathEscape(sessionID)+".evlog"), nil
}

func (s *FileStore) ensureIndexLocked(path string) (*fileSessionIndex, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			idx := &fileSessionIndex{eventIDToLine: make(map[string]int)}
			s.indexes[path] = idx
			return idx, nil
		}
		return nil, err
	}
	if idx := s.indexes[path]; idx != nil && idx.size == info.Size() && idx.modTime.Equal(info.ModTime()) {
		return idx, nil
	}
	idx, err := s.rebuildIndexLocked(path, info)
	if err != nil {
		delete(s.indexes, path)
		return nil, err
	}
	s.indexes[path] = idx
	return idx, nil
}

func (s *FileStore) rebuildIndexLocked(path string, info os.FileInfo) (*fileSessionIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	idx := &fileSessionIndex{
		size:          info.Size(),
		modTime:       info.ModTime(),
		eventIDToLine: make(map[string]int),
	}
	lineNo := 0
	var offset int64
	for {
		lineOffset := offset
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			offset += int64(len(line))
			lineNo++
			event, err := parseFileEventLine(line, lineNo)
			if err != nil {
				return nil, err
			}
			if _, dup := idx.eventIDToLine[event.payload.EventID]; dup {
				return nil, fmt.Errorf("%w: duplicate event_id %q at line %d", adk.ErrInvalidEventID, event.payload.EventID, lineNo)
			}
			idx.eventIDToLine[event.payload.EventID] = len(idx.offsets)
			idx.offsets = append(idx.offsets, lineOffset)
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			break
		}
		return nil, readErr
	}
	return idx, nil
}

func parseFileEventLine(line []byte, lineNo int) (fileEvent, error) {
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return fileEvent{}, fmt.Errorf("%w: corrupted trailing record at line %d", adk.ErrInvalidEventID, lineNo)
	}
	line = line[:len(line)-1]
	lineStr := string(line)

	firstTab := strings.IndexByte(lineStr, '\t')
	if firstTab < 0 {
		return fileEvent{}, fmt.Errorf("%w: missing tab separator at line %d", adk.ErrInvalidEventID, lineNo)
	}
	eventID := lineStr[:firstTab]
	if eventID == "" {
		return fileEvent{}, fmt.Errorf("%w: empty event_id at line %d", adk.ErrInvalidEventID, lineNo)
	}

	rest := lineStr[firstTab+1:]
	secondTab := strings.IndexByte(rest, '\t')
	if secondTab < 0 {
		return fileEvent{}, fmt.Errorf("%w: missing kind tab separator at line %d", adk.ErrInvalidEventID, lineNo)
	}
	return fileEvent{
		payload: adk.SessionEventPayload{
			EventID: eventID,
			Kind:    adk.SessionEventKind(rest[:secondTab]),
			Data:    []byte(rest[secondTab+1:]),
		},
	}, nil
}

func (s *FileStore) loadFileEventsForwardLocked(path string, idx *fileSessionIndex, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	start := 0
	if opts.After != "" {
		pos, ok := idx.eventIDToLine[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		start = pos + 1
	}
	if start > len(idx.offsets) {
		start = len(idx.offsets)
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &adk.LoadEventsResult{}, nil
		}
		return nil, err
	}
	defer f.Close()
	kindSet := buildKindSet(opts.Kinds)

	var out []adk.SessionEventPayload
	hasMore := false
	for i := start; i < len(idx.offsets); i++ {
		event, err := readFileEventAt(f, idx.offsets[i], i+1)
		if err != nil {
			return nil, err
		}
		if kindSet != nil {
			if _, match := kindSet[event.payload.Kind]; !match {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			hasMore = true
			break
		}
		out = append(out, copyFilePayload(event.payload))
	}

	var next string
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadEventsResult{Events: out, Next: next}, nil
}

func (s *FileStore) loadFileEventsReverseLocked(path string, idx *fileSessionIndex, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	end := len(idx.offsets)
	if opts.After != "" {
		pos, ok := idx.eventIDToLine[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		end = pos
	}
	if end <= 0 {
		return &adk.LoadEventsResult{}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &adk.LoadEventsResult{}, nil
		}
		return nil, err
	}
	defer f.Close()
	kindSet := buildKindSet(opts.Kinds)

	var out []adk.SessionEventPayload
	hasMore := false
	for i := end - 1; i >= 0; i-- {
		event, err := readFileEventAt(f, idx.offsets[i], i+1)
		if err != nil {
			return nil, err
		}
		if kindSet != nil {
			if _, match := kindSet[event.payload.Kind]; !match {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			hasMore = true
			break
		}
		out = append(out, copyFilePayload(event.payload))
	}

	var next string
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadEventsResult{Events: out, Next: next}, nil
}

func readFileEventAt(f *os.File, offset int64, lineNo int) (fileEvent, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fileEvent{}, err
	}
	reader := bufio.NewReader(f)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fileEvent{}, err
	}
	return parseFileEventLine(line, lineNo)
}

func copyFilePayload(src adk.SessionEventPayload) adk.SessionEventPayload {
	return adk.SessionEventPayload{
		EventID: src.EventID,
		Kind:    src.Kind,
		Data:    append([]byte{}, src.Data...),
	}
}
