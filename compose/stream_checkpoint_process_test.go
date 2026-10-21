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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type streamReplayFileStore struct{ dir string }

func (s streamReplayFileStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	b, e := os.ReadFile(filepath.Join(s.dir, fmt.Sprintf("%x.cp", sha256.Sum256([]byte(key)))))
	if errors.Is(e, os.ErrNotExist) {
		return nil, false, nil
	}
	return b, e == nil, e
}
func (s streamReplayFileStore) Set(_ context.Context, key string, b []byte) error {
	return os.WriteFile(filepath.Join(s.dir, fmt.Sprintf("%x.cp", sha256.Sum256([]byte(key)))), b, 0600)
}

func TestWorkflowStreamCheckpointBranchesAcrossProcesses(t *testing.T) {
	for _, name := range []string{"repeat_interrupt", "nested_workflow", "both_sources_interrupt"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			count := 2
			if name == "repeat_interrupt" {
				count = 3
			}
			for phase := 0; phase < count; phase++ {
				cmd := exec.CommandContext(streamTestContext(t), os.Args[0], "-test.run=^TestWorkflowStreamCheckpointBranches$/^"+name+"$", "-test.v")
				cmd.Env = append(os.Environ(), "EINO_STREAM_BRANCH_DIR="+root)
				out, err := cmd.CombinedOutput()
				t.Logf("phase %d: %s", phase, out)
				if err != nil {
					t.Fatal(err)
				}
				state, err := os.ReadFile(filepath.Join(root, "status"))
				if err != nil {
					t.Fatal(err)
				}
				want := "interrupt"
				if phase == count-1 {
					want = "success"
				}
				if string(state) != want {
					t.Fatalf("status=%s want=%s", state, want)
				}
			}
			log, err := os.ReadFile(filepath.Join(root, "counts"))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Fields(string(log))
			totals := map[string]int{}
			for _, line := range lines {
				var m map[string]int
				if err := json.Unmarshal([]byte(line), &m); err != nil {
					t.Fatal(err)
				}
				for k, v := range m {
					totals[k] += v
				}
			}
			wantB := 1
			if name == "both_sources_interrupt" {
				wantB = 2
			}
			if totals["A"] != count || totals["B"] != wantB || totals["End"] != count {
				t.Fatalf("counts=%v", totals)
			}
		})
	}
}
