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

package plantask

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/require"
)

func TestTaskUpdateValidatesCombinedDependenciesBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		seedEdge   bool
		wantErr    string
	}{
		{name: "direct cycle", args: `{"taskId":"1","addBlocks":["2"],"addBlockedBy":["2"]}`, wantErr: "cyclic dependency"},
		{name: "transitive cycle", seedEdge: true,
			args: `{"taskId":"1","addBlocks":["2"],"addBlockedBy":["3"]}`, wantErr: "cyclic dependency"},
		{name: "invalid blocker", args: `{"taskId":"1","addBlocks":["2"],"addBlockedBy":["invalid"]}`, wantErr: "invalid format"},
		{name: "valid chain", args: `{"taskId":"1","addBlocks":["2"],"addBlockedBy":["3"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			backend := newInMemoryBackend()
			const baseDir = "/tmp/tasks"
			before := make(map[string]string)
			for _, id := range []string{"1", "2", "3"} {
				tk := &task{ID: id, Subject: "Task " + id, Status: taskStatusPending}
				if tc.seedEdge {
					if id == "2" {
						tk.Blocks = []string{"3"}
					} else if id == "3" {
						tk.BlockedBy = []string{"2"}
					}
				}
				content, err := sonic.MarshalString(tk)
				require.NoError(t, err)
				path := filepath.Join(baseDir, id+".json")
				require.NoError(t, backend.Write(ctx, &WriteRequest{FilePath: path, Content: content}))
				before[path] = content
			}
			update := newTaskUpdateTool(backend, baseDir, &sync.Mutex{})
			_, err := update.InvokableRun(ctx, tc.args)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				for path, want := range before {
					got, readErr := backend.Read(ctx, &ReadRequest{FilePath: path})
					require.NoError(t, readErr)
					require.Equal(t, want, got.Content, "rejected updates must leave %s intact", path)
				}
				return
			}
			require.NoError(t, err)
			tasks, err := listTasks(ctx, backend, baseDir)
			require.NoError(t, err)
			byID := make(map[string]*task)
			for _, tk := range tasks {
				byID[tk.ID] = tk
			}
			require.Equal(t, []string{"2"}, byID["1"].Blocks)
			require.Equal(t, []string{"3"}, byID["1"].BlockedBy)
			require.Equal(t, []string{"1"}, byID["2"].BlockedBy)
			require.Equal(t, []string{"1"}, byID["3"].Blocks)
		})
	}
}
