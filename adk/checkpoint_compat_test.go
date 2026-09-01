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
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	checkpointCompatGenerateEnv = "EINO_GENERATE_CHECKPOINT_COMPAT"
	checkpointCompatDir         = "testdata/checkpoint_compat/main_60e1d992"
	checkpointCompatPayloadSize = 32 << 10
)

var checkpointCompatFrozenSHA256 = map[string]string{
	"single_invoke":            "139efb8e7252d95c36b6c835f5db97609425f52c24d4408f614c62c7ecf2fd05",
	"single_stream":            "e91af8cc44cee9341cc729244a4b55ae991793b3fbfe1b1fcd39d25c9c0a25fb",
	"cancel_after_model":       "108a6182394be730df5a41268cf2f72bd0e164eb81de8e9abc4ecf1aa1fea222",
	"agent_tool_depth_1":       "c127efcfc43c5aab076fa2bcdecd230156278aa5f2260d7d78289e41dc5c4e3e",
	"agent_tool_depth_2":       "a67737bf4ef31699699d8c7795db3d3de5b4ddd99944725c2973d8db0a387fd0",
	"agent_tool_depth_3":       "99205c85c6410c0de91f5a729334df18d5ee1659ab25a4619b13ad1d95ffb6e2",
	"parallel_6":               "7a99fe9dadf84a0c860f391bdcdb9af338f28a002f1c5c4239d188695a70ce14",
	"parallel_6_single_target": "e4c69dbdebed9e61ca6985efe53de5faa0c83b026b0817fe26b8a5475a685d47",
	"parallel_6_multi_target":  "e4f71623bc502ba6d0247b94024933bbdd50bb22ae280b9664ba6fbd7253a6b6",
	"payload_content":          "f47ff20c1beb9ae453386a129a6f2c69bbbc4ff7b9d8041ef77f1f8ca212d676",
	"payload_reasoning":        "8cde19a5b15686734259057b838e08987fdaa63b96de34b798ec2e81ef6c3e51",
	"payload_arguments":        "753e1d940b36230ba90c7ddda47e2dfef177cd39c57b4bb4e48fb90abf76e8fc",
	"payload_extra":            "4c575e9a0645846b22d1c704a7fa1e648d0dbcd1fa116aceddf194e915d67bd4",
	"payload_multimodal":       "9cba820f852ceee44c2bc92c9058fc2bdde63d9a5e39dc9bc3430d69fce1a8e7",
}

type checkpointCompatManifest struct {
	ProducerCommit string                    `json:"producer_commit"`
	Fixtures       []checkpointCompatFixture `json:"fixtures"`
}

type checkpointCompatFixture struct {
	Name               string   `json:"name"`
	File               string   `json:"file"`
	SHA256             string   `json:"sha256"`
	Depth              int      `json:"depth"`
	ParallelChildren   int      `json:"parallel_children,omitempty"`
	Streaming          bool     `json:"streaming,omitempty"`
	Cancel             bool     `json:"cancel,omitempty"`
	ResumeTargetCount  int      `json:"resume_target_count,omitempty"`
	ExpectedInterrupts int      `json:"expected_interrupts,omitempty"`
	PayloadField       string   `json:"payload_field"`
	PayloadSize        int      `json:"payload_size"`
	InterruptIDs       []string `json:"interrupt_ids"`
	InterruptAddresses []string `json:"interrupt_addresses"`
}

type checkpointCompatStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newCheckpointCompatStore() *checkpointCompatStore {
	return &checkpointCompatStore{data: make(map[string][]byte)}
}

func (s *checkpointCompatStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *checkpointCompatStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[key]
	return append([]byte(nil), value...), ok, nil
}

type checkpointCompatModel struct {
	toolNames    []string
	payload      string
	payloadField string
}

func (m *checkpointCompatModel) Generate(_ context.Context, input []*schema.Message,
	_ ...model.Option) (*schema.Message, error) {
	return m.response(input), nil
}

func (m *checkpointCompatModel) Stream(_ context.Context, input []*schema.Message,
	_ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.response(input)}), nil
}

func (m *checkpointCompatModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *checkpointCompatModel) response(input []*schema.Message) *schema.Message {
	if len(input) > 0 && input[len(input)-1].Role == schema.Tool {
		return schema.AssistantMessage("completed", nil)
	}
	calls := make([]schema.ToolCall, 0, len(m.toolNames))
	for i, name := range m.toolNames {
		arguments := `{"request":"continue"}`
		if m.payloadField == "tool_arguments" {
			arguments = fmt.Sprintf(`{"request":%q}`, m.payload)
		}
		calls = append(calls, schema.ToolCall{
			ID: fmt.Sprintf("call-%d", i),
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: arguments,
			},
		})
	}
	msg := schema.AssistantMessage("", calls)
	switch m.payloadField {
	case "content":
		msg.Content = m.payload
	case "reasoning":
		msg.ReasoningContent = m.payload
	case "extra":
		msg.Extra = map[string]any{"checkpoint_payload": m.payload}
	case "multimodal":
		payload := m.payload
		msg.AssistantGenMultiContent = []schema.MessageOutputPart{{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageOutputImage{
				MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &payload,
					MIMEType:   "image/png",
				},
			},
		}}
	}
	return msg
}

type checkpointCompatInterruptTool struct {
	name string
}

type checkpointCompatCompletionTool struct {
	name string
}

func (t *checkpointCompatCompletionTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "complete after cancel resume"}, nil
}

func (t *checkpointCompatCompletionTool) InvokableRun(context.Context, string,
	...componenttool.Option) (string, error) {
	return "completed tool", nil
}

func (t *checkpointCompatInterruptTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "interrupt for checkpoint compatibility"}, nil
}

func (t *checkpointCompatInterruptTool) InvokableRun(ctx context.Context, _ string,
	_ ...componenttool.Option) (string, error) {
	wasInterrupted, hasState, _ := componenttool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return "", componenttool.StatefulInterrupt(ctx, t.name, "interrupted")
	}
	isTarget, hasData, data := componenttool.GetResumeContext[string](ctx)
	if isTarget {
		if hasData {
			return data, nil
		}
		return "resumed", nil
	}
	if !hasState {
		return "", fmt.Errorf("checkpoint compatibility tool %s lost state", t.name)
	}
	return "", componenttool.StatefulInterrupt(ctx, t.name, "re-interrupted")
}

func newCheckpointCompatAgent(t *testing.T, depth, parallelChildren int, payloadField string,
	payloadSize int) Agent {
	t.Helper()
	payload := strings.Repeat("x", payloadSize)
	if parallelChildren > 0 {
		tools := make([]componenttool.BaseTool, 0, parallelChildren)
		names := make([]string, 0, parallelChildren)
		for i := 0; i < parallelChildren; i++ {
			name := fmt.Sprintf("ParallelChild%d", i)
			child := newCheckpointCompatNestedAgent(t, name, 0, payloadField, payload)
			tools = append(tools, NewAgentTool(context.Background(), child))
			names = append(names, name)
		}
		return newCheckpointCompatChatModelAgent(t, "ParallelParent", names, tools, "", "")
	}
	return newCheckpointCompatNestedAgent(t, "RootAgent", depth, payloadField, payload)
}

func newCheckpointCompatCancelResumeAgent(t *testing.T) Agent {
	t.Helper()
	const toolName = "CancelCompletionTool"
	return newCheckpointCompatChatModelAgent(t, "CancelAgent", []string{toolName},
		[]componenttool.BaseTool{&checkpointCompatCompletionTool{name: toolName}}, "", "")
}

func newCheckpointCompatNestedAgent(t *testing.T, name string, depth int, payloadField,
	payload string) Agent {
	t.Helper()
	if depth == 0 {
		toolName := name + "Interrupt"
		return newCheckpointCompatChatModelAgent(t, name, []string{toolName},
			[]componenttool.BaseTool{&checkpointCompatInterruptTool{name: toolName}},
			payloadField, payload)
	}
	childName := fmt.Sprintf("%sChild%d", name, depth)
	child := newCheckpointCompatNestedAgent(t, childName, depth-1, payloadField, payload)
	return newCheckpointCompatChatModelAgent(t, name, []string{childName},
		[]componenttool.BaseTool{NewAgentTool(context.Background(), child)}, "", "")
}

func newCheckpointCompatChatModelAgent(t *testing.T, name string, toolNames []string,
	tools []componenttool.BaseTool, payloadField, payload string) Agent {
	t.Helper()
	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:        name,
		Description: "checkpoint compatibility agent",
		Model: &checkpointCompatModel{
			toolNames:    toolNames,
			payload:      payload,
			payloadField: payloadField,
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
	require.NoError(t, err)
	return agent
}

func captureCheckpointCompatFixture(t *testing.T, spec checkpointCompatFixture) ([]byte, []string, []string) {
	t.Helper()
	if spec.Cancel {
		return captureCheckpointCompatCancelFixture(t, spec)
	}
	store := newCheckpointCompatStore()
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent: newCheckpointCompatAgent(t, spec.Depth, spec.ParallelChildren,
			spec.PayloadField, spec.PayloadSize),
		EnableStreaming: spec.Streaming,
		CheckPointStore: store,
	})
	iter := runner.Query(context.Background(), "start", WithCheckPointID(spec.Name))
	var interruptIDs []string
	var interruptAddresses []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
				interruptIDs = append(interruptIDs, interruptCtx.ID)
				interruptAddresses = append(interruptAddresses, interruptCtx.Address.String())
			}
		}
	}
	raw, ok, err := store.Get(context.Background(), spec.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotEmpty(t, interruptIDs)
	return raw, interruptIDs, interruptAddresses
}

func captureCheckpointCompatCancelFixture(t *testing.T, spec checkpointCompatFixture) ([]byte, []string, []string) {
	t.Helper()
	ctx := context.Background()
	const toolName = "CancelCompletionTool"
	blockingModel := newBlockingChatModel(toolCallMsg(toolCall("cancel-call", toolName, `{}`)))
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "CancelAgent",
		Description: "checkpoint compatibility cancel agent",
		Model:       blockingModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{&checkpointCompatCompletionTool{name: toolName}},
			},
		},
	})
	require.NoError(t, err)
	store := newCheckpointCompatStore()
	runner := NewRunner(ctx, RunnerConfig{Agent: agent, CheckPointStore: store})
	cancelOpt, cancelFn := WithCancel()
	iter := runner.Query(ctx, "start", WithCheckPointID(spec.Name), cancelOpt)

	select {
	case <-blockingModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel fixture model did not start")
	}
	done := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		done <- handle.Wait()
	}()
	close(blockingModel.unblockCh)
	require.NoError(t, <-done)

	var interruptIDs []string
	var interruptAddresses []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
				interruptIDs = append(interruptIDs, interruptCtx.ID)
				interruptAddresses = append(interruptAddresses, interruptCtx.Address.String())
			}
		}
	}
	raw, ok, err := store.Get(ctx, spec.Name)
	require.NoError(t, err)
	require.True(t, ok)
	return raw, interruptIDs, interruptAddresses
}

func writeCheckpointCompatFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	file, err := os.Create(path)
	require.NoError(t, err)
	defer file.Close()
	writer, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	require.NoError(t, err)
	_, err = writer.Write(raw)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
}

func readCheckpointCompatFixture(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	require.NoError(t, err)
	return raw
}

func checkpointCompatSpecs() []checkpointCompatFixture {
	return []checkpointCompatFixture{
		{Name: "single_invoke", File: "single_invoke.bin.gz", PayloadField: "content"},
		{Name: "single_stream", File: "single_stream.bin.gz", Streaming: true, PayloadField: "content"},
		{Name: "cancel_after_model", File: "cancel_after_model.bin.gz", Cancel: true, PayloadField: "content"},
		{Name: "agent_tool_depth_1", File: "agent_tool_depth_1.bin.gz", Depth: 1, PayloadField: "content"},
		{Name: "agent_tool_depth_2", File: "agent_tool_depth_2.bin.gz", Depth: 2, PayloadField: "content"},
		{Name: "agent_tool_depth_3", File: "agent_tool_depth_3.bin.gz", Depth: 3, PayloadField: "content"},
		{Name: "parallel_6", File: "parallel_6.bin.gz", ParallelChildren: 6, PayloadField: "content", ResumeTargetCount: 6},
		{Name: "parallel_6_single_target", File: "parallel_6_single_target.bin.gz", ParallelChildren: 6, PayloadField: "content", ResumeTargetCount: 1, ExpectedInterrupts: 5},
		{Name: "parallel_6_multi_target", File: "parallel_6_multi_target.bin.gz", ParallelChildren: 6, PayloadField: "content", ResumeTargetCount: 2, ExpectedInterrupts: 4},
		{Name: "payload_content", File: "payload_content.bin.gz", Depth: 1, PayloadField: "content", PayloadSize: checkpointCompatPayloadSize},
		{Name: "payload_reasoning", File: "payload_reasoning.bin.gz", Depth: 1, PayloadField: "reasoning", PayloadSize: checkpointCompatPayloadSize},
		{Name: "payload_arguments", File: "payload_arguments.bin.gz", Depth: 1, PayloadField: "tool_arguments", PayloadSize: checkpointCompatPayloadSize},
		{Name: "payload_extra", File: "payload_extra.bin.gz", Depth: 1, PayloadField: "extra", PayloadSize: checkpointCompatPayloadSize},
		{Name: "payload_multimodal", File: "payload_multimodal.bin.gz", Depth: 1, PayloadField: "multimodal", PayloadSize: checkpointCompatPayloadSize},
	}
}

func TestGenerateCheckpointCompatFixtures(t *testing.T) {
	if os.Getenv(checkpointCompatGenerateEnv) != "1" {
		t.Skip("set EINO_GENERATE_CHECKPOINT_COMPAT=1 to regenerate fixtures")
	}
	manifest := checkpointCompatManifest{ProducerCommit: "60e1d9929cb65c8c4814b66fba2854e29b730114"}
	for _, spec := range checkpointCompatSpecs() {
		raw, interruptIDs, interruptAddresses := captureCheckpointCompatFixture(t, spec)
		spec.InterruptIDs = interruptIDs
		spec.InterruptAddresses = interruptAddresses
		sum := sha256.Sum256(raw)
		spec.SHA256 = hex.EncodeToString(sum[:])
		writeCheckpointCompatFixture(t, filepath.Join(checkpointCompatDir, spec.File), raw)
		manifest.Fixtures = append(manifest.Fixtures, spec)
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(checkpointCompatDir, "manifest.json"),
		append(data, '\n'), 0o644))
}

func TestCheckpointBackwardCompatMain60e1d992(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(checkpointCompatDir, "manifest.json"))
	require.NoError(t, err)
	var manifest checkpointCompatManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, "60e1d9929cb65c8c4814b66fba2854e29b730114", manifest.ProducerCommit)
	require.Len(t, manifest.Fixtures, len(checkpointCompatFrozenSHA256))

	seen := make(map[string]struct{}, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			frozenSHA, exists := checkpointCompatFrozenSHA256[fixture.Name]
			require.True(t, exists, "fixture is not part of the frozen set")
			require.Equal(t, frozenSHA, fixture.SHA256,
				"frozen fixture metadata changed; add a new fixture version instead")
			seen[fixture.Name] = struct{}{}
			raw := readCheckpointCompatFixture(t, filepath.Join(checkpointCompatDir, fixture.File))
			sum := sha256.Sum256(raw)
			require.Equal(t, fixture.SHA256, hex.EncodeToString(sum[:]))

			store := newCheckpointCompatStore()
			require.NoError(t, store.Set(context.Background(), fixture.Name, raw))
			agent := newCheckpointCompatAgent(t, fixture.Depth, fixture.ParallelChildren,
				fixture.PayloadField, fixture.PayloadSize)
			if fixture.Cancel {
				agent = newCheckpointCompatCancelResumeAgent(t)
			}
			runner := NewRunner(context.Background(), RunnerConfig{
				Agent:           agent,
				CheckPointStore: store,
			})
			targetCount := fixture.ResumeTargetCount
			if targetCount == 0 && !fixture.Cancel {
				targetCount = len(fixture.InterruptIDs)
			}
			targets := make(map[string]any, targetCount)
			for _, id := range fixture.InterruptIDs[:targetCount] {
				targets[id] = "resumed"
			}
			iter, err := runner.ResumeWithParams(context.Background(), fixture.Name,
				&ResumeParams{Targets: targets})
			require.NoError(t, err)
			var eventCount int
			remainingInterrupts := make(map[string]struct{})
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				eventCount++
				require.NoError(t, event.Err)
				if event.Action != nil && event.Action.Interrupted != nil {
					for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
						remainingInterrupts[interruptCtx.Address.String()] = struct{}{}
					}
				}
			}
			assert.Positive(t, eventCount)
			expectedInterrupts := make(map[string]struct{}, fixture.ExpectedInterrupts)
			for _, address := range fixture.InterruptAddresses[targetCount:] {
				expectedInterrupts[address] = struct{}{}
			}
			assert.Equal(t, expectedInterrupts, remainingInterrupts)
		})
	}
	require.Len(t, seen, len(checkpointCompatFrozenSHA256))
}

func TestCheckpointLegacyReaderMain60e1d992(t *testing.T) {
	readerBin := buildCheckpointCompatLegacyReader(t)

	fixtureDir, err := filepath.Abs(checkpointCompatDir)
	require.NoError(t, err)
	for _, fixture := range checkpointCompatSpecs() {
		t.Run(fixture.Name, func(t *testing.T) {
			cmd := exec.Command(readerBin, "-fixture-dir", fixtureDir, "-fixture", fixture.Name)
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
		})
	}
}

func buildCheckpointCompatLegacyReader(t *testing.T) string {
	t.Helper()
	readerDir := filepath.Join("testdata", "checkpoint_compat", "legacy_reader")
	readerBin := filepath.Join(t.TempDir(), "checkpoint-legacy-reader")
	build := exec.Command("go", "build", "-o", readerBin, ".")
	build.Dir = readerDir
	build.Env = append(os.Environ(), "GOWORK=off")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	return readerBin
}

func TestAttack_CheckpointCompatRejectsTruncatedBytes(t *testing.T) {
	const fixtureName = "single_invoke"
	raw := readCheckpointCompatFixture(t,
		filepath.Join(checkpointCompatDir, "single_invoke.bin.gz"))
	require.Greater(t, len(raw), 2)
	raw = raw[:len(raw)/2]

	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), fixtureName, raw))
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent:           newCheckpointCompatAgent(t, 0, 0, "content", 0),
		CheckPointStore: store,
	})
	_, err := runner.Resume(context.Background(), fixtureName)
	require.ErrorContains(t, err, "failed to decode checkpoint")
}
