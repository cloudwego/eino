/*
 * Copyright 2024 CloudWeGo Authors
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

package compose_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/compose"
)

type streamCallCount struct{ value int32 }

func (c *streamCallCount) Add(delta int32) int32 { return atomic.AddInt32(&c.value, delta) }
func (c *streamCallCount) Load() int32           { return atomic.LoadInt32(&c.value) }
func streamTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

type streamReplayStore struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *streamReplayStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	return append([]byte(nil), v...), ok, nil
}

func (s *streamReplayStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func requireStreamInterrupt(t *testing.T, err error) *compose.InterruptInfo {
	t.Helper()
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) == 0 {
		t.Fatalf("expected interrupt, got %v", err)
	}
	return info
}
