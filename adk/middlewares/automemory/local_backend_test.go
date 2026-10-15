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

package automemory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalBackend_ConcurrentWritesUseIndependentTemporaryFiles(t *testing.T) {
	ctx := context.Background()
	backend := NewLocalBackend()
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.md")

	const writers = 32
	start := make(chan struct{})
	errs := make(chan error, writers)
	contents := make(map[string]struct{}, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		content := fmt.Sprintf("writer-%02d:%s", i, strings.Repeat(string(rune('a'+i%26)), 256*1024))
		contents[content] = struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- backend.Write(ctx, &WriteRequest{FilePath: path, Content: content})
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	_, ok := contents[string(raw)]
	require.True(t, ok, "final file must equal one complete writer payload")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".tmp")
	}
}

func TestLocalBackend_ConcurrentEditsPreserveSuccessfulUpdates(t *testing.T) {
	ctx := context.Background()
	backend := NewLocalBackend()
	path := filepath.Join(t.TempDir(), "memory.md")

	const edits = 32
	tokens := make([]string, 0, edits)
	for i := 0; i < edits; i++ {
		tokens = append(tokens, fmt.Sprintf("token-%02d", i))
	}
	require.NoError(t, backend.Write(ctx, &WriteRequest{
		FilePath: path,
		Content:  strings.Join(tokens, "\n"),
	}))

	start := make(chan struct{})
	errs := make(chan error, edits)
	var wg sync.WaitGroup
	for _, token := range tokens {
		token := token
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- backend.Edit(ctx, &EditRequest{
				FilePath:  path,
				OldString: token,
				NewString: strings.ToUpper(token),
			})
		}()
	}

	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	content := string(raw)
	for _, token := range tokens {
		require.Contains(t, content, strings.ToUpper(token))
		require.NotContains(t, content, token)
	}
}
