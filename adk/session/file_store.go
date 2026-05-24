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
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// FileStore is a process-local, file-backed implementation of adk.SessionStore.
// Each session is stored as one JSONL file under the configured directory:
//
//	<root>/<url.PathEscape(sessionID)>.jsonl
//
// Each line is exactly one JSON-encoded SessionEvent payload. AppendEvents
// rejects raw CR/LF bytes in payloads to preserve JSONL framing. FileStore does
// not implement CheckPointStore; runner checkpoints should use a dedicated
// checkpoint store.
//
// FileStore synchronizes access within the current process. It does not provide
// cross-process write safety. A crash or OS write failure may leave a trailing
// partial line; future LoadEvents or AppendEvents calls report that as log
// corruption with adk.ErrInvalidEventID.
type FileStore struct {
	dir string
	mu  sync.Mutex
}

type fileEvent struct {
	payload []byte
	eventID string
}

// NewFileStore creates a file-backed SessionStore rooted at dir.
func NewFileStore(dir string) (*FileStore, error) {
	if dir == "" {
		return nil, errorsNewEmptySessionStoreDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileStore{dir: dir}, nil
}

func errorsNewEmptySessionStoreDir() error {
	return fmt.Errorf("adk/session: file store dir is empty")
}

func errorsNewEmptySessionID() error {
	return fmt.Errorf("adk/session: sessionID is empty")
}

// AppendEvents appends events to the session's JSONL event log.
//
// Payloads must be valid single-line JSON objects with a non-empty event_id.
// Duplicate event IDs are skipped with first-write-wins semantics, including
// duplicates within the same batch.
func (s *FileStore) AppendEvents(_ context.Context, sessionID string, events [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.sessionPath(sessionID)
	if err != nil {
		return err
	}
	pending, err := preflightFileEvents(events)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	_, existing, err := s.readAllEventsLocked(path)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	for _, event := range pending {
		if _, dup := existing[event.eventID]; dup {
			continue
		}
		if _, err := out.Write(event.payload); err != nil {
			return err
		}
		if _, err := out.Write([]byte("\n")); err != nil {
			return err
		}
		existing[event.eventID] = len(existing)
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
	events, idx, err := s.readAllEventsLocked(path)
	if err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &adk.LoadEventsRequest{}
	}
	if opts.Reverse {
		return loadFileEventsReverse(events, idx, opts)
	}
	return loadFileEventsForward(events, idx, opts)
}

func (s *FileStore) sessionPath(sessionID string) (string, error) {
	if sessionID == "" {
		return "", errorsNewEmptySessionID()
	}
	return filepath.Join(s.dir, url.PathEscape(sessionID)+".jsonl"), nil
}

func preflightFileEvents(events [][]byte) ([]fileEvent, error) {
	seen := make(map[string]struct{}, len(events))
	pending := make([]fileEvent, 0, len(events))
	for _, payload := range events {
		eventID, err := parseFileEventPayload(payload)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[eventID]; dup {
			continue
		}
		seen[eventID] = struct{}{}
		pending = append(pending, fileEvent{
			payload: append([]byte{}, payload...),
			eventID: eventID,
		})
	}
	return pending, nil
}

func parseFileEventPayload(payload []byte) (string, error) {
	if bytes.ContainsAny(payload, "\r\n") {
		return "", fmt.Errorf("%w: payload contains raw line delimiter", adk.ErrInvalidEventID)
	}
	var h eventHeader
	if err := json.Unmarshal(payload, &h); err != nil {
		return "", fmt.Errorf("%w: %v", adk.ErrInvalidEventID, err)
	}
	if h.EventID == "" {
		return "", adk.ErrInvalidEventID
	}
	return h.EventID, nil
}

func (s *FileStore) readAllEventsLocked(path string) ([]fileEvent, map[string]int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, map[string]int{}, nil
		}
		return nil, nil, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	events := make([]fileEvent, 0)
	idx := make(map[string]int)
	lineNo := 0
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if line[len(line)-1] != '\n' {
				return nil, nil, fmt.Errorf("%w: corrupted trailing record at line %d", adk.ErrInvalidEventID, lineNo)
			}
			if line[len(line)-1] == '\n' {
				line = line[:len(line)-1]
			}
			eventID, err := parseFileEventPayload(line)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: corrupted record at line %d", err, lineNo)
			}
			if _, dup := idx[eventID]; dup {
				return nil, nil, fmt.Errorf("%w: duplicate event_id %q at line %d", adk.ErrInvalidEventID, eventID, lineNo)
			}
			events = append(events, fileEvent{
				payload: append([]byte{}, line...),
				eventID: eventID,
			})
			idx[eventID] = len(events) - 1
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			break
		}
		return nil, nil, readErr
	}
	return events, idx, nil
}

func loadFileEventsForward(events []fileEvent, idx map[string]int, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	start := 0
	if opts.After != "" {
		pos, ok := idx[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		start = pos + 1
	}
	if start > len(events) {
		start = len(events)
	}

	end := len(events)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	out := make([][]byte, end-start)
	for i := range out {
		out[i] = append([]byte{}, events[start+i].payload...)
	}

	var next string
	if end < len(events) && end > 0 {
		next = events[end-1].eventID
	}
	return &adk.LoadEventsResult{Events: out, Next: next}, nil
}

func loadFileEventsReverse(events []fileEvent, idx map[string]int, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	end := len(events)
	if opts.After != "" {
		pos, ok := idx[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		end = pos
	}
	if end <= 0 {
		return &adk.LoadEventsResult{}, nil
	}

	count := end
	if opts.Limit > 0 && opts.Limit < count {
		count = opts.Limit
	}

	start := end - count
	out := make([][]byte, count)
	for i := 0; i < count; i++ {
		out[i] = append([]byte{}, events[end-1-i].payload...)
	}

	var next string
	if start > 0 {
		next = events[start].eventID
	}
	return &adk.LoadEventsResult{Events: out, Next: next}, nil
}
