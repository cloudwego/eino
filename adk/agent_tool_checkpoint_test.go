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
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentToolInterruptStateV1(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		got, err := decodeAgentToolInterruptState([]byte("legacy"), "agent")
		require.NoError(t, err)
		require.Equal(t, []byte("legacy"), got)
	})
	t.Run("v1", func(t *testing.T) {
		got, err := decodeAgentToolInterruptState(&agentToolInterruptStateV1{
			Version:          agentToolInterruptStateVersion,
			BridgeCheckpoint: []byte("v1"),
		}, "agent")
		require.NoError(t, err)
		require.Equal(t, []byte("v1"), got)
	})
	t.Run("unsupported_version", func(t *testing.T) {
		_, err := decodeAgentToolInterruptState(&agentToolInterruptStateV1{
			Version:          agentToolInterruptStateVersion + 1,
			BridgeCheckpoint: []byte("v1"),
		}, "agent")
		require.ErrorContains(t, err, "unsupported interrupt state version")
	})
	t.Run("empty_checkpoint", func(t *testing.T) {
		_, err := decodeAgentToolInterruptState(&agentToolInterruptStateV1{
			Version: agentToolInterruptStateVersion,
		}, "agent")
		require.ErrorContains(t, err, "empty bridge checkpoint")
	})
	t.Run("invalid_type", func(t *testing.T) {
		_, err := decodeAgentToolInterruptState("invalid", "agent")
		require.ErrorContains(t, err, "invalid interrupt state type")
	})
}

func TestAgentToolCheckpointV1SizeAndLegacyFailure(t *testing.T) {
	readerBin := buildCheckpointCompatLegacyReader(t)
	for _, streaming := range []bool{false, true} {
		name := "invoke"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			spec := checkpointCompatFixture{
				Name:         "agent_tool_v1_320k_" + name,
				File:         "agent_tool_v1_320k_" + name + ".bin.gz",
				Depth:        1,
				Streaming:    streaming,
				PayloadField: "content",
				PayloadSize:  320 << 10,
			}
			raw, interruptIDs, interruptAddresses := captureCheckpointCompatFixture(t, spec)
			t.Logf("checkpoint bytes: %d", len(raw))
			require.LessOrEqual(t, len(raw), 4_800_000)
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
			require.Contains(t, string(output), "name not registered for interface")
		})
	}
}

func TestAgentToolCheckpointV1ParallelTargetedResume(t *testing.T) {
	spec := checkpointCompatFixture{
		Name:             "agent_tool_v1_parallel",
		ParallelChildren: 6,
		PayloadField:     "content",
	}
	raw, interruptIDs, _ := captureCheckpointCompatFixture(t, spec)
	resumeCheckpointCompatCandidate(t, spec, raw, interruptIDs, 1, 5)
}

func resumeCheckpointCompatCandidate(t *testing.T, spec checkpointCompatFixture, raw []byte,
	interruptIDs []string, targetCount, expectedInterrupts int) {
	t.Helper()
	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), spec.Name, raw))
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent: newCheckpointCompatAgent(t, spec.Depth, spec.ParallelChildren,
			spec.PayloadField, spec.PayloadSize),
		EnableStreaming: spec.Streaming,
		CheckPointStore: store,
	})
	targets := make(map[string]any, targetCount)
	for _, id := range interruptIDs[:targetCount] {
		targets[id] = "resumed"
	}
	iter, err := runner.ResumeWithParams(context.Background(), spec.Name,
		&ResumeParams{Targets: targets})
	require.NoError(t, err)
	remaining := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			remaining += len(event.Action.Interrupted.InterruptContexts)
		}
	}
	require.Equal(t, expectedInterrupts, remaining)
}
