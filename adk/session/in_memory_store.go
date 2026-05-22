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
    "context"
    "strconv"
    "sync"

    "github.com/cloudwego/eino/adk"
)

// InMemoryStore is a thread-safe, in-memory implementation of adk.SessionStore
// and CheckPointStore (with Delete support). Suitable for testing and
// single-process deployments where durability is not required.
type InMemoryStore struct {
    mu          sync.Mutex
    events      map[string][][]byte
    checkpoints map[string][]byte
}

// NewInMemoryStore creates a new InMemoryStore.
func NewInMemoryStore() *InMemoryStore {
    return &InMemoryStore{
        events:      make(map[string][][]byte),
        checkpoints: make(map[string][]byte),
    }
}

// AppendEvents appends events to the session's event log.
func (s *InMemoryStore) AppendEvents(_ context.Context, sessionID string, events [][]byte) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    for _, e := range events {
        s.events[sessionID] = append(s.events[sessionID], append([]byte{}, e...))
    }
    return nil
}

// LoadEvents loads events with pagination and direction support.
func (s *InMemoryStore) LoadEvents(_ context.Context, sessionID string, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
    s.mu.Lock()
    defer s.mu.Unlock()

    all := s.events[sessionID]
    if opts == nil {
        opts = &adk.LoadEventsRequest{}
    }

    if opts.Reverse {
        return s.loadReverse(all, opts)
    }
    return s.loadForward(all, opts)
}

func (s *InMemoryStore) loadForward(all [][]byte, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
    start := 0
    if opts.After != "" {
        idx, err := strconv.Atoi(opts.After)
        if err != nil {
            return nil, err
        }
        start = idx
    }
    if start > len(all) {
        start = len(all)
    }

    end := len(all)
    if opts.Limit > 0 && start+opts.Limit < end {
        end = start + opts.Limit
    }

    out := make([][]byte, end-start)
    for i := range out {
        out[i] = append([]byte{}, all[start+i]...)
    }

    var next string
    if end < len(all) {
        next = strconv.Itoa(end)
    }
    return &adk.LoadEventsResult{Events: out, Next: next}, nil
}

func (s *InMemoryStore) loadReverse(all [][]byte, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
    // In reverse mode, After is the cursor indicating how far back we've read.
    // It represents the index of the last element returned (exclusive from the top).
    // First call (After=""): start from the end.
    // Subsequent calls: start from the After position (exclusive, moving backwards).
    end := len(all)
    if opts.After != "" {
        idx, err := strconv.Atoi(opts.After)
        if err != nil {
            return nil, err
        }
        end = idx
    }
    if end > len(all) {
        end = len(all)
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
        out[i] = append([]byte{}, all[end-1-i]...)
    }

    var next string
    if start > 0 {
        next = strconv.Itoa(start)
    }
    return &adk.LoadEventsResult{Events: out, Next: next}, nil
}

// Set stores a checkpoint value.
func (s *InMemoryStore) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.checkpoints[checkPointID] = append([]byte{}, checkPoint...)
    return nil
}

// Get retrieves a checkpoint value. Returns an independent copy.
func (s *InMemoryStore) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    v, ok := s.checkpoints[checkPointID]
    if !ok {
        return nil, false, nil
    }
    return append([]byte{}, v...), true, nil
}

// Delete removes a checkpoint.
func (s *InMemoryStore) Delete(_ context.Context, checkPointID string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    delete(s.checkpoints, checkPointID)
    return nil
}
