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
	"github.com/cloudwego/eino/schema"
)

// FileStoreConfig configures FileStore.
type FileStoreConfig struct {
	// EventSerializer encodes typed session events before storage. Defaults to
	// schema.HumanReadableSerializer. Output must not contain raw CR/LF bytes.
	EventSerializer schema.Serializer
}

// FileStore is a process-local, file-backed implementation of adk.SessionEventStore.
// Each session is stored as one event log file under the configured directory:
//
//	<root>/<url.PathEscape(sessionID)>.evlog
//
// Each line is formatted as: <EventID>\t<Kind>\t<Data>\n
// where Data is the raw serialized bytes written directly to the line.
//
// IMPORTANT: FileStore requires that serialized event data does NOT contain raw
// newline (\n) or carriage-return (\r) characters, because these would
// corrupt the line-oriented file format. The default HumanReadableSerializer
// (compact JSON) satisfies this constraint. Serializers that may emit \n or \r
// in their output (e.g. GobSerializer, raw protobuf) are NOT compatible with
// FileStore — use InMemoryStore or a custom store implementation instead.
// AppendEvents will return an error if Data contains \n or \r.
//
// FileStore does not implement CheckPointStore; runner checkpoints should use a
// dedicated checkpoint store.
//
// FileStore synchronizes access within the current process. It does not provide
// cross-process write safety.
type FileStore[M adk.MessageType] struct {
	dir        string
	serializer schema.Serializer
	mu         sync.Mutex
	indexes    map[string]*fileSessionIndex
}

type fileEvent struct {
	eventID string
	kind    adk.SessionEventKind
	data    []byte
}

type fileSessionIndex struct {
	size          int64
	modTime       time.Time
	offsets       []int64
	eventIDToLine map[string]int
}

// NewFileStore creates a file-backed SessionService rooted at dir.
func NewFileStore[M adk.MessageType](dir string, cfg *FileStoreConfig) (*FileStore[M], error) {
	if dir == "" {
		return nil, errorsNewEmptyFileStoreDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore[M]{
		dir:        dir,
		serializer: normalizeFileSerializer(cfg),
		indexes:    make(map[string]*fileSessionIndex),
	}, nil
}

// NewFileSessionService creates a local, process-scoped service backed by FileStore.
func NewFileSessionService[M adk.MessageType](dir string, cfg *FileStoreConfig) (adk.SessionService[M], error) {
	store, err := NewFileStore[M](dir, cfg)
	if err != nil {
		return nil, err
	}
	return adk.NewLocalSessionService[M](store), nil
}

func errorsNewEmptyFileStoreDir() error {
	return fmt.Errorf("adk/session: file store dir is empty")
}

func errorsNewEmptySessionID() error {
	return fmt.Errorf("adk/session: sessionID is empty")
}

// AppendEvents appends events to the session's event log.
//
// Each SessionEvent.EventID MUST be non-empty. The expected tail and event
// append are validated under the same process-local lock. Duplicate event IDs
// are accepted only for exact batch replay after a successful prior append.
func (s *FileStore[M]) AppendEvents(_ context.Context, req *adk.AppendSessionEventsRequest[M]) (*adk.AppendSessionEventsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req == nil {
		req = &adk.AppendSessionEventsRequest[M]{}
	}
	sessionID := req.SessionID
	events := req.Events

	path, err := s.sessionPath(sessionID)
	if err != nil {
		return nil, err
	}

	// Validate incoming events and dedup within batch.
	seen := make(map[string]struct{}, len(events))
	pending := make([]fileEvent, 0, len(events))
	for _, e := range events {
		if e == nil || e.EventID == "" {
			return nil, adk.ErrInvalidEventID
		}
		if _, dup := seen[e.EventID]; dup {
			return nil, adk.ErrDuplicateEventID
		}
		seen[e.EventID] = struct{}{}
		if normalizeErr := adk.NormalizeSessionEventKind(e); normalizeErr != nil {
			return nil, normalizeErr
		}
		data, marshalErr := s.serializer.Marshal(e)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if bytes.ContainsAny(data, "\r\n") {
			return nil, fmt.Errorf("adk/session: FileStore requires serialized event data without raw CR/LF; use a line-safe serializer")
		}
		pending = append(pending, fileEvent{eventID: e.EventID, kind: e.Kind, data: data})
	}
	if len(pending) == 0 {
		return &adk.AppendSessionEventsResult{SessionTailEventID: req.ExpectedSessionTailEventID}, nil
	}

	idx, err := s.ensureIndexLocked(path)
	if err != nil {
		return nil, err
	}
	currentTail := fileCurrentTailLocked(idx)
	if currentTail != req.ExpectedSessionTailEventID {
		if s.isExactFileBatchReplayLocked(path, idx, req.ExpectedSessionTailEventID, pending) {
			return &adk.AppendSessionEventsResult{SessionTailEventID: currentTail}, nil
		}
		return nil, adk.ErrSessionTailMismatch
	}

	var out *os.File
	for _, event := range pending {
		if _, dup := idx.eventIDToLine[event.eventID]; dup {
			return nil, adk.ErrDuplicateEventID
		}
		if out == nil {
			out, err = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return nil, err
			}
			defer out.Close()
		}
		line := fmt.Sprintf("%s\t%s\t%s\n", event.eventID, event.kind, event.data)
		n, err := out.WriteString(line)
		if err != nil {
			return nil, err
		}
		idx.eventIDToLine[event.eventID] = len(idx.offsets)
		idx.offsets = append(idx.offsets, idx.size)
		idx.size += int64(n)
	}
	if out != nil {
		info, err := out.Stat()
		if err != nil {
			return nil, err
		}
		idx.size = info.Size()
		idx.modTime = info.ModTime()
	}
	return &adk.AppendSessionEventsResult{SessionTailEventID: fileCurrentTailLocked(idx)}, nil
}

// LoadEvents loads events with pagination and direction support.
func (s *FileStore[M]) LoadEvents(_ context.Context, opts *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if opts == nil {
		opts = &adk.LoadSessionEventsRequest{}
	}
	sessionID := opts.SessionID
	path, err := s.sessionPath(sessionID)
	if err != nil {
		return nil, err
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

func (s *FileStore[M]) sessionPath(sessionID string) (string, error) {
	if sessionID == "" {
		return "", errorsNewEmptySessionID()
	}
	return filepath.Join(s.dir, url.PathEscape(sessionID)+".evlog"), nil
}

func (s *FileStore[M]) ensureIndexLocked(path string) (*fileSessionIndex, error) {
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

func (s *FileStore[M]) rebuildIndexLocked(path string, info os.FileInfo) (*fileSessionIndex, error) {
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
			if _, dup := idx.eventIDToLine[event.eventID]; dup {
				return nil, fmt.Errorf("%w: duplicate event_id %q at line %d", adk.ErrInvalidEventID, event.eventID, lineNo)
			}
			idx.eventIDToLine[event.eventID] = len(idx.offsets)
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
		eventID: eventID,
		kind:    adk.SessionEventKind(rest[:secondTab]),
		data:    []byte(rest[secondTab+1:]),
	}, nil
}

func (s *FileStore[M]) loadFileEventsForwardLocked(path string, idx *fileSessionIndex, opts *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
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
			return &adk.LoadSessionEventsResult[M]{SessionTailEventID: fileCurrentTailLocked(idx)}, nil
		}
		return nil, err
	}
	defer f.Close()
	kindSet := buildKindSet(opts.Kinds)

	var out []*adk.SessionEvent[M]
	hasMore := false
	for i := start; i < len(idx.offsets); i++ {
		event, err := readFileEventAt(f, idx.offsets[i], i+1)
		if err != nil {
			return nil, err
		}
		if kindSet != nil {
			if _, match := kindSet[event.kind]; !match {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			hasMore = true
			break
		}
		decoded, err := s.decodeFileEvent(event)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded)
	}

	var next string
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadSessionEventsResult[M]{Events: out, Next: next, SessionTailEventID: fileCurrentTailLocked(idx)}, nil
}

func (s *FileStore[M]) loadFileEventsReverseLocked(path string, idx *fileSessionIndex, opts *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	end := len(idx.offsets)
	if opts.After != "" {
		pos, ok := idx.eventIDToLine[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		end = pos
	}
	if end <= 0 {
		return &adk.LoadSessionEventsResult[M]{SessionTailEventID: fileCurrentTailLocked(idx)}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &adk.LoadSessionEventsResult[M]{SessionTailEventID: fileCurrentTailLocked(idx)}, nil
		}
		return nil, err
	}
	defer f.Close()
	kindSet := buildKindSet(opts.Kinds)

	var out []*adk.SessionEvent[M]
	hasMore := false
	for i := end - 1; i >= 0; i-- {
		event, err := readFileEventAt(f, idx.offsets[i], i+1)
		if err != nil {
			return nil, err
		}
		if kindSet != nil {
			if _, match := kindSet[event.kind]; !match {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			hasMore = true
			break
		}
		decoded, err := s.decodeFileEvent(event)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded)
	}

	var next string
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadSessionEventsResult[M]{Events: out, Next: next, SessionTailEventID: fileCurrentTailLocked(idx)}, nil
}

func fileCurrentTailLocked(idx *fileSessionIndex) string {
	if idx == nil || len(idx.offsets) == 0 {
		return ""
	}
	for id, line := range idx.eventIDToLine {
		if line == len(idx.offsets)-1 {
			return id
		}
	}
	return ""
}

func (s *FileStore[M]) isExactFileBatchReplayLocked(path string, idx *fileSessionIndex, expectedTail string, pending []fileEvent) bool {
	if len(pending) == 0 {
		return fileCurrentTailLocked(idx) == expectedTail
	}
	start := 0
	if expectedTail != "" {
		pos, ok := idx.eventIDToLine[expectedTail]
		if !ok {
			return false
		}
		start = pos + 1
	}
	if start+len(pending) != len(idx.offsets) {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	for i, event := range pending {
		existing, err := readFileEventAt(f, idx.offsets[start+i], start+i+1)
		if err != nil || existing.eventID != event.eventID {
			return false
		}
	}
	return true
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

func (s *FileStore[M]) decodeFileEvent(src fileEvent) (*adk.SessionEvent[M], error) {
	var event adk.SessionEvent[M]
	if err := s.serializer.Unmarshal(src.data, &event); err != nil {
		return nil, err
	}
	if err := adk.NormalizeSessionEventKind(&event); err != nil {
		return nil, err
	}
	if event.EventID != src.eventID || event.Kind != src.kind {
		return nil, fmt.Errorf("adk/session: file event metadata mismatch for event_id %q", src.eventID)
	}
	return &event, nil
}

func normalizeFileSerializer(cfg *FileStoreConfig) schema.Serializer {
	if cfg != nil && cfg.EventSerializer != nil {
		return cfg.EventSerializer
	}
	return &schema.HumanReadableSerializer{}
}
