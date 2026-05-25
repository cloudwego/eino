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

	"github.com/cloudwego/eino/adk"
)

// FileStore is a process-local, file-backed implementation of adk.SessionStore.
// Each session is stored as one event log file under the configured directory:
//
//	<root>/<url.PathEscape(sessionID)>.evlog
//
// Each line is formatted as: <EventID>\t<Data>\n
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
	dir string
	mu  sync.Mutex
}

type fileEvent struct {
	payload adk.SessionEventPayload
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
		if _, dup := existing[event.EventID]; dup {
			continue
		}
		if bytes.ContainsAny(event.Data, "\r\n") {
			return fmt.Errorf("adk/session: FileStore requires Data without raw CR/LF; use a line-safe serializer (e.g. HumanReadableSerializer)")
		}
		line := fmt.Sprintf("%s\t%s\n", event.EventID, event.Data)
		if _, err := out.WriteString(line); err != nil {
			return err
		}
		existing[event.EventID] = len(existing)
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
	return filepath.Join(s.dir, url.PathEscape(sessionID)+".evlog"), nil
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
			// Strip trailing newline
			line = line[:len(line)-1]
			lineStr := string(line)

			// Split on first tab
			tabIdx := strings.IndexByte(lineStr, '\t')
			if tabIdx < 0 {
				return nil, nil, fmt.Errorf("%w: missing tab separator at line %d", adk.ErrInvalidEventID, lineNo)
			}
			eventID := lineStr[:tabIdx]
			if eventID == "" {
				return nil, nil, fmt.Errorf("%w: empty event_id at line %d", adk.ErrInvalidEventID, lineNo)
			}
			data := []byte(lineStr[tabIdx+1:])

			if _, dup := idx[eventID]; dup {
				return nil, nil, fmt.Errorf("%w: duplicate event_id %q at line %d", adk.ErrInvalidEventID, eventID, lineNo)
			}
			events = append(events, fileEvent{
				payload: adk.SessionEventPayload{EventID: eventID, Data: data},
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

	out := make([]adk.SessionEventPayload, end-start)
	for i := range out {
		src := events[start+i].payload
		out[i] = adk.SessionEventPayload{
			EventID: src.EventID,
			Data:    append([]byte{}, src.Data...),
		}
	}

	var next string
	if end < len(events) && end > 0 {
		next = events[end-1].payload.EventID
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
	out := make([]adk.SessionEventPayload, count)
	for i := 0; i < count; i++ {
		src := events[end-1-i].payload
		out[i] = adk.SessionEventPayload{
			EventID: src.EventID,
			Data:    append([]byte{}, src.Data...),
		}
	}

	var next string
	if start > 0 {
		next = events[start].payload.EventID
	}
	return &adk.LoadEventsResult{Events: out, Next: next}, nil
}
