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

package compose

import (
	"bytes"
	"encoding/gob"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

type checkpointForwardCompatAnyEnvelope struct {
	Value any
}

func TestCheckpointV1ConcreteTypesFailLoudlyInLegacyReader(t *testing.T) {
	readerDir := filepath.Join("..", "adk", "testdata", "checkpoint_compat", "legacy_reader")
	readerBin := filepath.Join(t.TempDir(), "checkpoint-legacy-reader")
	build := exec.Command("go", "build", "-o", readerBin, ".")
	build.Dir = readerDir
	build.Env = append(os.Environ(), "GOWORK=off")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))

	tests := []struct {
		name           string
		value          any
		registeredName string
	}{
		{
			name:           "layout_sentinel",
			value:          &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
			registeredName: "_eino_checkpoint_layout_v1",
		},
		{
			name: "tools_state",
			value: &toolsInterruptAndRerunStateV1{
				Version:   toolsInterruptAndRerunStateVersionV1,
				Role:      schema.Assistant,
				ToolCalls: []schema.ToolCall{{ID: "call"}},
			},
			registeredName: "_eino_compose_tools_interrupt_and_rerun_state_v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, gob.NewEncoder(&buf).Encode(&checkpointForwardCompatAnyEnvelope{
				Value: tt.value,
			}))
			path := filepath.Join(t.TempDir(), "value.gob")
			require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
			cmd := exec.Command(readerBin, "-gob-any-file", path)
			output, err := cmd.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(output), "name not registered for interface")
			require.Contains(t, string(output), tt.registeredName)
		})
	}
}
