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

package adk

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckpointLayoutV1SizeAndLegacyFailure(t *testing.T) {
	readerBin := buildCheckpointCompatLegacyReader(t)
	tests := []struct {
		name      string
		depth     int
		payload   int
		sizeLimit int
	}{
		{name: "single_no_subgraph", payload: 320 << 10, sizeLimit: 1_800_000},
		{name: "agent_tool_320k", depth: 1, payload: 320 << 10, sizeLimit: 2_500_000},
		{name: "agent_tool_1m", depth: 1, payload: 1 << 20, sizeLimit: 8_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := checkpointCompatFixture{
				Name:         tt.name,
				File:         tt.name + ".bin.gz",
				Depth:        tt.depth,
				PayloadField: "content",
				PayloadSize:  tt.payload,
			}
			raw, interruptIDs, interruptAddresses := captureCheckpointCompatFixture(t, spec)
			t.Logf("checkpoint bytes: %d", len(raw))
			require.LessOrEqual(t, len(raw), tt.sizeLimit)
			resumeCheckpointCompatCandidate(t, spec, raw, interruptIDs, len(interruptIDs), 0)

			spec.InterruptIDs = interruptIDs
			spec.InterruptAddresses = interruptAddresses
			tmpDir := t.TempDir()
			writeCheckpointCompatFixture(t, filepath.Join(tmpDir, spec.File), raw)
			manifestData, err := json.Marshal(checkpointCompatManifest{
				ProducerCommit: "candidate",
				Fixtures:       []checkpointCompatFixture{spec},
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "manifest.json"), manifestData, 0o644))

			cmd := exec.Command(readerBin, "-fixture-dir", tmpDir, "-fixture", spec.Name)
			output, err := cmd.CombinedOutput()
			require.Error(t, err)
			if tt.depth == 0 {
				require.Contains(t, string(output), "_eino_checkpoint_layout_v1")
			} else {
				require.Contains(t, string(output), "name not registered for interface")
			}
		})
	}
}
