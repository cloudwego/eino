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
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	checkpointCompatDir             = "testdata/checkpoint_compat/main_60e1d992"
	checkpointCompatFormatV0        = 0
	checkpointCompatImplicitFixture = "parallel_6"
	checkpointCompatProducerCommit  = "60e1d9929cb65c8c4814b66fba2854e29b730114"
	checkpointCompatProducerVersion = "v0.9.18"
	checkpointCompatGeneratorCommit = "3e3e994e7b10955c336ae38a610ddbff5e371521"
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
	ProducerCommit          string                    `json:"producer_commit"`
	ProducerVersion         string                    `json:"producer_version"`
	GeneratorCommit         string                    `json:"generator_commit"`
	CheckpointFormatVersion *int                      `json:"checkpoint_format_version"`
	Fixtures                []checkpointCompatFixture `json:"fixtures"`
}

type checkpointCompatResumeOutcome struct {
	TerminalOutputs             int               `json:"terminal_outputs"`
	RemainingInterruptAddresses []string          `json:"remaining_interrupt_addresses"`
	RemainingInterruptIDs       []string          `json:"-"`
	RemainingAddressesByID      map[string]string `json:"-"`
	ResumeMethod                string            `json:"resume_method"`
}

type checkpointGobSchemaOld struct {
	Stable  string
	Removed string
}

type checkpointGobSchemaNew struct {
	Stable string
	Added  string
}

type checkpointGobSchemaString struct {
	Value string
}

type checkpointGobSchemaStruct struct {
	Value struct {
		Text string
	}
}

type checkpointCompatAnyEnvelope struct {
	Value any
}

type checkpointCompatFixture struct {
	Name               string   `json:"name"`
	File               string   `json:"file"`
	SHA256             string   `json:"sha256"`
	Depth              int      `json:"depth"`
	ParallelChildren   int      `json:"parallel_children,omitempty"`
	Streaming          bool     `json:"streaming,omitempty"`
	Cancel             bool     `json:"cancel,omitempty"`
	ImplicitResume     bool     `json:"implicit_resume,omitempty"`
	ResumeTargetCount  int      `json:"resume_target_count,omitempty"`
	ExpectedInterrupts int      `json:"expected_interrupts,omitempty"`
	PayloadField       string   `json:"payload_field"`
	PayloadSize        int      `json:"payload_size"`
	InterruptIDs       []string `json:"interrupt_ids"`
	InterruptAddresses []string `json:"interrupt_addresses"`
	StableDepthNames   bool     `json:"-"`
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
	name           string
	implicitResume bool
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
	if !hasState {
		return "", fmt.Errorf("checkpoint compatibility tool %s lost state", t.name)
	}
	if t.implicitResume {
		return "resumed", nil
	}
	isTarget, hasData, data := componenttool.GetResumeContext[string](ctx)
	if isTarget {
		if hasData {
			return data, nil
		}
		return "resumed", nil
	}
	return "", componenttool.StatefulInterrupt(ctx, t.name, "re-interrupted")
}

func newCheckpointCompatAgent(t *testing.T, depth, parallelChildren int, payloadField string,
	payloadSize int, implicitResume ...bool) Agent {
	t.Helper()
	payload := strings.Repeat("x", payloadSize)
	resumeImplicitly := len(implicitResume) > 0 && implicitResume[0]
	if parallelChildren > 0 {
		tools := make([]componenttool.BaseTool, 0, parallelChildren)
		names := make([]string, 0, parallelChildren)
		for i := 0; i < parallelChildren; i++ {
			name := fmt.Sprintf("ParallelChild%d", i)
			child := newCheckpointCompatNestedAgent(t, name, 0, payloadField, payload, resumeImplicitly)
			tools = append(tools, NewAgentTool(context.Background(), child))
			names = append(names, name)
		}
		return newCheckpointCompatChatModelAgent(t, "ParallelParent", names, tools, "", "")
	}
	return newCheckpointCompatNestedAgent(t, "RootAgent", depth, payloadField, payload, resumeImplicitly)
}

func newCheckpointCompatCancelResumeAgent(t *testing.T) Agent {
	t.Helper()
	const toolName = "CancelCompletionTool"
	return newCheckpointCompatChatModelAgent(t, "CancelAgent", []string{toolName},
		[]componenttool.BaseTool{&checkpointCompatCompletionTool{name: toolName}}, "", "")
}

func newCheckpointCompatNestedAgent(t *testing.T, name string, depth int, payloadField,
	payload string, implicitResume bool) Agent {
	t.Helper()
	if depth == 0 {
		toolName := name + "Interrupt"
		return newCheckpointCompatChatModelAgent(t, name, []string{toolName},
			[]componenttool.BaseTool{&checkpointCompatInterruptTool{
				name:           toolName,
				implicitResume: implicitResume,
			}},
			payloadField, payload)
	}
	childName := fmt.Sprintf("%sChild%d", name, depth)
	child := newCheckpointCompatNestedAgent(t, childName, depth-1, payloadField, payload, implicitResume)
	return newCheckpointCompatChatModelAgent(t, name, []string{childName},
		[]componenttool.BaseTool{NewAgentTool(context.Background(), child)}, "", "")
}

func newCheckpointCompatStableDepthAgent(t *testing.T, depth int, payloadField string,
	payloadSize int) Agent {
	t.Helper()
	payload := strings.Repeat("x", payloadSize)
	var build func(int) Agent
	build = func(level int) Agent {
		name := fmt.Sprintf("DepthAgent%02d", level)
		if level == depth {
			toolName := fmt.Sprintf("DepthTool%02d", level)
			return newCheckpointCompatChatModelAgent(t, name, []string{toolName},
				[]componenttool.BaseTool{&checkpointCompatInterruptTool{name: toolName}},
				payloadField, payload)
		}
		child := build(level + 1)
		childName := fmt.Sprintf("DepthAgent%02d", level+1)
		return newCheckpointCompatChatModelAgent(t, name, []string{childName},
			[]componenttool.BaseTool{NewAgentTool(context.Background(), child)}, "", "")
	}
	return build(0)
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
	agent := newCheckpointCompatAgent(t, spec.Depth, spec.ParallelChildren,
		spec.PayloadField, spec.PayloadSize)
	if spec.StableDepthNames {
		agent = newCheckpointCompatStableDepthAgent(
			t, spec.Depth, spec.PayloadField, spec.PayloadSize)
	}
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent:           agent,
		EnableStreaming: spec.Streaming,
		CheckPointStore: store,
	})
	query := "start"
	if spec.PayloadField == "user_query" {
		query = strings.Repeat("x", spec.PayloadSize)
	}
	iter := runner.Query(context.Background(), query, WithCheckPointID(spec.Name))
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
	modelOutput := toolCallMsg(toolCall("cancel-call", toolName, `{}`))
	if spec.PayloadField == "content" && spec.PayloadSize > 0 {
		modelOutput.Content = strings.Repeat("x", spec.PayloadSize)
	}
	blockingModel := newBlockingChatModel(modelOutput)
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
	cancelCtx := getCommonOptions(nil, cancelOpt).cancelCtx
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
	select {
	case <-cancelCtx.cancelChan:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel request was not registered")
	}
	close(blockingModel.unblockCh)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancel request did not complete")
	}

	var interruptIDs []string
	var interruptAddresses []string
	var cancelEvents int
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			var cancelErr *CancelError
			require.ErrorAs(t, event.Err, &cancelErr)
			require.NotNil(t, cancelErr.Info)
			require.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
			cancelEvents++
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
				interruptIDs = append(interruptIDs, interruptCtx.ID)
				interruptAddresses = append(interruptAddresses, interruptCtx.Address.String())
			}
		}
	}
	require.Equal(t, 1, cancelEvents)
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

func collectCheckpointCompatResumeOutcome(t *testing.T,
	iter *AsyncIterator[*AgentEvent]) checkpointCompatResumeOutcome {
	t.Helper()
	var eventCount int
	outcome := checkpointCompatResumeOutcome{
		RemainingAddressesByID: make(map[string]string),
	}
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		eventCount++
		require.NoError(t, event.Err)
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg, err := event.Output.MessageOutput.GetMessage()
			require.NoError(t, err)
			if msg != nil && msg.Role == schema.Assistant && msg.Content == "completed" {
				outcome.TerminalOutputs++
			}
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
				outcome.RemainingInterruptAddresses = append(
					outcome.RemainingInterruptAddresses, interruptCtx.Address.String())
				outcome.RemainingInterruptIDs = append(
					outcome.RemainingInterruptIDs, interruptCtx.ID)
				outcome.RemainingAddressesByID[interruptCtx.ID] = interruptCtx.Address.String()
			}
		}
	}
	require.Positive(t, eventCount)
	sort.Strings(outcome.RemainingInterruptAddresses)
	return outcome
}

func TestCollectCheckpointCompatResumeOutcomePreservesAddressMultiplicity(t *testing.T) {
	iter, generator := NewAsyncIteratorPair[*AgentEvent]()
	address := Address{{Type: AddressSegmentAgent, ID: "duplicate"}}
	generator.Send(&AgentEvent{Action: &AgentAction{Interrupted: &InterruptInfo{
		InterruptContexts: []*InterruptCtx{
			{Address: address},
			{Address: address},
		},
	}}})
	generator.Close()

	outcome := collectCheckpointCompatResumeOutcome(t, iter)
	require.Equal(t, []string{"agent:duplicate", "agent:duplicate"},
		outcome.RemainingInterruptAddresses)
}

func TestCheckpointLegacyAgentToolPartialResumeReinterruptsWithAbsoluteState(t *testing.T) {
	manifestData, err := os.ReadFile(filepath.Join(checkpointCompatDir, "manifest.json"))
	require.NoError(t, err)
	var manifest checkpointCompatManifest
	require.NoError(t, json.Unmarshal(manifestData, &manifest))
	fixtures := make(map[string]checkpointCompatFixture, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		fixtures[fixture.File] = fixture
	}

	tests := []struct {
		name      string
		fixture   string
		streaming bool
		v1        bool
	}{
		{name: "legacy_invoke_nested", fixture: "parallel_6.bin.gz"},
		{name: "v1_invoke_nested", fixture: "parallel_6.bin.gz", v1: true},
		{name: "legacy_stream_nested", fixture: "parallel_6.bin.gz", streaming: true},
		{name: "v1_stream_nested", fixture: "parallel_6.bin.gz", streaming: true, v1: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, exists := fixtures[tt.fixture]
			require.True(t, exists)
			require.Len(t, fixture.InterruptAddresses, 6)
			require.Len(t, fixture.InterruptIDs, 6)
			activeIDs := cloneSlice(fixture.InterruptIDs)
			raw := readCheckpointCompatFixture(t, filepath.Join(checkpointCompatDir, tt.fixture))
			raw = rewriteCheckpointCompatAgentToolStates(t, raw, tt.streaming, tt.v1)
			legacyCount, v1Count, v2Count := countCheckpointCompatAgentToolStates(t, raw)
			if tt.v1 {
				require.Zero(t, legacyCount)
				require.Equal(t, 2*len(fixture.InterruptIDs), v1Count)
			} else {
				require.Equal(t, 2*len(fixture.InterruptIDs), legacyCount)
				require.Zero(t, v1Count)
			}
			require.Zero(t, v2Count)

			const checkpointID = "legacy-agent-tool-reinterrupt"
			store := newCheckpointCompatStore()
			require.NoError(t, store.Set(context.Background(), checkpointID, raw))
			runner := NewRunner(context.Background(), RunnerConfig{
				Agent: newCheckpointCompatAgent(
					t, fixture.Depth, fixture.ParallelChildren,
					fixture.PayloadField, fixture.PayloadSize),
				EnableStreaming: tt.streaming,
				CheckPointStore: store,
			})

			addressByID := make(map[string]string, len(activeIDs))
			remainingAddresses := make(map[string]struct{}, len(activeIDs))
			for i, id := range activeIDs {
				addressByID[id] = fixture.InterruptAddresses[i]
				remainingAddresses[fixture.InterruptAddresses[i]] = struct{}{}
			}
			expectedActiveCounts := []int{6, 4, 2, 0}
			stageTargetCounts := []int{2, 2}
			for stage, targetCount := range stageTargetCounts {
				require.Len(t, activeIDs, expectedActiveCounts[stage])
				targets := make(map[string]any, targetCount)
				for _, targetID := range activeIDs[:targetCount] {
					targets[targetID] = "resumed"
					delete(remainingAddresses, addressByID[targetID])
				}
				iter, resumeErr := runner.ResumeWithParams(context.Background(), checkpointID,
					&ResumeParams{Targets: targets})
				require.NoError(t, resumeErr)
				partial := collectCheckpointCompatResumeOutcome(t, iter)
				require.Len(t, remainingAddresses, expectedActiveCounts[stage+1])
				require.Len(t, partial.RemainingInterruptIDs, expectedActiveCounts[stage+1])
				require.Len(t, partial.RemainingAddressesByID, expectedActiveCounts[stage+1])
				expectedAddresses := make([]string, 0, len(remainingAddresses))
				for address := range remainingAddresses {
					expectedAddresses = append(expectedAddresses, address)
				}
				requireCheckpointCompatResumeOutcome(
					t, partial, len(remainingAddresses), expectedAddresses)
				activeIDs = partial.RemainingInterruptIDs
				addressByID = partial.RemainingAddressesByID

				rewritten, exists, getErr := store.Get(context.Background(), checkpointID)
				require.NoError(t, getErr)
				require.True(t, exists)
				legacyCount, v1Count, v2Count =
					countCheckpointCompatAgentToolStates(t, rewritten)
				require.Zero(t, legacyCount)
				require.Equal(t, len(remainingAddresses), v1Count)
				require.Zero(t, v2Count,
					"absolute AgentTool checkpoints must not be relabeled as V2")
				_, _, _, loadErr := runnerLoadCheckPointImpl(
					store, context.Background(), checkpointID)
				require.NoError(t, loadErr,
					"stage %d checkpoint must remain readable after re-interrupt", stage+1)
			}

			require.Len(t, activeIDs, expectedActiveCounts[len(stageTargetCounts)])
			targets := make(map[string]any, len(activeIDs))
			for _, id := range activeIDs {
				targets[id] = "resumed"
			}
			iter, err := runner.ResumeWithParams(context.Background(), checkpointID,
				&ResumeParams{Targets: targets})
			require.NoError(t, err)
			outcome := collectCheckpointCompatResumeOutcome(t, iter)
			require.Len(t, outcome.RemainingInterruptIDs,
				expectedActiveCounts[len(expectedActiveCounts)-1])
			requireCheckpointCompatResumeOutcome(t, outcome, 0, nil)
		})
	}
}

func rewriteCheckpointCompatAgentToolStates(t *testing.T, raw []byte,
	streaming, useV1 bool) []byte {
	t.Helper()
	var rewriteRunner func([]byte) ([]byte, bool)
	rewriteRunner = func(data []byte) ([]byte, bool) {
		var checkpoint serialization
		if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&checkpoint); err != nil ||
			checkpoint.RunCtx == nil {
			return nil, false
		}
		checkpoint.EnableStreaming = streaming
		for id, state := range checkpoint.InterruptID2State {
			composeData, ok := state.State.([]byte)
			if !ok {
				continue
			}
			rewritten, err := compose.TransformCheckpointValues(composeData, &gobSerializer{},
				func(_ compose.NodePath, location compose.CheckpointValueLocation,
					value any) (any, bool, error) {
					if location.Kind != compose.CheckpointValueInterruptState {
						return value, false, nil
					}
					var bridge []byte
					switch value := value.(type) {
					case []byte:
						bridge = value
					case *agentToolInterruptStateV1:
						if value != nil {
							bridge = value.BridgeCheckpoint
						}
					case *agentToolInterruptStateV2:
						if value != nil {
							bridge = value.BridgeCheckpoint
						}
					}
					child, childOK := rewriteRunner(bridge)
					if !childOK {
						return value, false, nil
					}
					if useV1 {
						return &agentToolInterruptStateV1{
							Version:          agentToolInterruptStateVersionV1,
							BridgeCheckpoint: child,
						}, true, nil
					}
					return child, true, nil
				})
			require.NoError(t, err)
			state.State = rewritten
			checkpoint.InterruptID2State[id] = state
		}
		encoded, err := encodeRunnerCheckpoint(&checkpoint)
		require.NoError(t, err)
		return encoded, true
	}
	rewritten, ok := rewriteRunner(raw)
	require.True(t, ok)
	return rewritten
}

func countCheckpointCompatAgentToolStates(t *testing.T, raw []byte) (
	legacy, v1, v2 int) {
	t.Helper()
	var countRunner func([]byte)
	countRunner = func(data []byte) {
		var checkpoint serialization
		require.NoError(t, gob.NewDecoder(bytes.NewReader(data)).Decode(&checkpoint))
		for _, state := range checkpoint.InterruptID2State {
			composeData, ok := state.State.([]byte)
			if !ok {
				continue
			}
			require.NoError(t, compose.WalkCheckpointValues(composeData, &gobSerializer{},
				func(_ compose.NodePath, location compose.CheckpointValueLocation,
					value any) error {
					if location.Kind != compose.CheckpointValueInterruptState {
						return nil
					}
					switch value := value.(type) {
					case []byte:
						var child serialization
						if err := gob.NewDecoder(bytes.NewReader(value)).Decode(&child); err == nil &&
							child.RunCtx != nil {
							legacy++
							countRunner(value)
						}
					case *agentToolInterruptStateV1:
						if value != nil {
							v1++
							countRunner(value.BridgeCheckpoint)
						}
					case *agentToolInterruptStateV2:
						if value != nil {
							v2++
							countRunner(value.BridgeCheckpoint)
						}
					}
					return nil
				}))
		}
	}
	countRunner(raw)
	return legacy, v1, v2
}

func requireCheckpointCompatResumeOutcome(t *testing.T, outcome checkpointCompatResumeOutcome,
	expectedInterrupts int, expectedAddresses []string) {
	t.Helper()
	expectedTerminalOutputs := 0
	if expectedInterrupts == 0 {
		expectedTerminalOutputs = 1
	}
	require.Equal(t, expectedTerminalOutputs, outcome.TerminalOutputs)

	wantAddresses := append([]string(nil), expectedAddresses...)
	sort.Strings(wantAddresses)
	require.Len(t, wantAddresses, expectedInterrupts)
	require.Equal(t, wantAddresses, outcome.RemainingInterruptAddresses)
}

func TestCheckpointBackwardCompatMain60e1d992(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(checkpointCompatDir, "manifest.json"))
	require.NoError(t, err)
	var manifest checkpointCompatManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, checkpointCompatProducerCommit, manifest.ProducerCommit)
	require.Equal(t, checkpointCompatProducerVersion, manifest.ProducerVersion)
	require.Equal(t, checkpointCompatGeneratorCommit, manifest.GeneratorCommit)
	require.NotNil(t, manifest.CheckpointFormatVersion)
	require.Equal(t, checkpointCompatFormatV0, *manifest.CheckpointFormatVersion)
	require.Len(t, manifest.Fixtures, len(checkpointCompatFrozenSHA256))

	seen := make(map[string]struct{}, len(manifest.Fixtures))
	for _, fixture := range manifest.Fixtures {
		frozenSHA, exists := checkpointCompatFrozenSHA256[fixture.Name]
		require.True(t, exists, "fixture is not part of the frozen set")
		require.NotContains(t, seen, fixture.Name, "duplicate fixture")
		require.Equal(t, frozenSHA, fixture.SHA256,
			"frozen fixture metadata changed; add a new fixture version instead")
		seen[fixture.Name] = struct{}{}
	}
	require.Len(t, seen, len(checkpointCompatFrozenSHA256))

	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			raw := readCheckpointCompatFixture(t, filepath.Join(checkpointCompatDir, fixture.File))
			sum := sha256.Sum256(raw)
			require.Equal(t, fixture.SHA256, hex.EncodeToString(sum[:]))

			implicitResume := fixture.Name == checkpointCompatImplicitFixture
			require.Equal(t, implicitResume, fixture.ImplicitResume,
				"only %s may declare implicit resume", checkpointCompatImplicitFixture)
			store := newCheckpointCompatStore()
			require.NoError(t, store.Set(context.Background(), fixture.Name, raw))
			agent := newCheckpointCompatAgent(t, fixture.Depth, fixture.ParallelChildren,
				fixture.PayloadField, fixture.PayloadSize, implicitResume)
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
			var iter *AsyncIterator[*AgentEvent]
			if implicitResume {
				iter, err = runner.Resume(context.Background(), fixture.Name)
			} else {
				targets := make(map[string]any, targetCount)
				for _, id := range fixture.InterruptIDs[:targetCount] {
					targets[id] = "resumed"
				}
				iter, err = runner.ResumeWithParams(context.Background(), fixture.Name,
					&ResumeParams{Targets: targets})
			}
			require.NoError(t, err)
			outcome := collectCheckpointCompatResumeOutcome(t, iter)
			requireCheckpointCompatResumeOutcome(t, outcome, fixture.ExpectedInterrupts,
				fixture.InterruptAddresses[targetCount:])
		})
	}
}

func TestCheckpointLegacyReaderMain60e1d992(t *testing.T) {
	readerBin := buildCheckpointCompatLegacyReader(t)

	fixtureDir, err := filepath.Abs(checkpointCompatDir)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(checkpointCompatDir, "manifest.json"))
	require.NoError(t, err)
	var manifest checkpointCompatManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	var implicitResumeFixtures, targetedResumeFixtures int
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			cmd := exec.Command(readerBin, "-fixture-dir", fixtureDir, "-fixture", fixture.Name)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			require.NoError(t, err, "stderr: %s\nstdout: %s", stderr.String(), output)
			var outcome checkpointCompatResumeOutcome
			require.NoError(t, json.Unmarshal(output, &outcome), string(output))
			targetCount := fixture.ResumeTargetCount
			if targetCount == 0 && !fixture.Cancel {
				targetCount = len(fixture.InterruptIDs)
			}
			implicitResume := fixture.Name == checkpointCompatImplicitFixture
			expectedResumeMethod := "resume_with_params"
			if implicitResume {
				expectedResumeMethod = "resume"
				implicitResumeFixtures++
			} else {
				targetedResumeFixtures++
			}
			require.Equal(t, implicitResume, fixture.ImplicitResume,
				"only %s may declare implicit resume", checkpointCompatImplicitFixture)
			require.Equal(t, expectedResumeMethod, outcome.ResumeMethod)
			requireCheckpointCompatResumeOutcome(t, outcome, fixture.ExpectedInterrupts,
				fixture.InterruptAddresses[targetCount:])
		})
	}
	require.Equal(t, 1, implicitResumeFixtures)
	require.Equal(t, len(manifest.Fixtures)-1, targetedResumeFixtures)
}

func buildCheckpointCompatLegacyReader(t *testing.T) string {
	t.Helper()
	readerDir := filepath.Join("testdata", "checkpoint_compat", "legacy_reader")
	readerBin := filepath.Join(t.TempDir(), "checkpoint-legacy-reader")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"),
		"build", "-o", readerBin, ".")
	build.Dir = readerDir
	build.Env = checkpointCompatSubprocessEnv()
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	return readerBin
}

func checkpointCompatSubprocessEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+2)
	for _, value := range env {
		if strings.HasPrefix(value, "GOWORK=") ||
			strings.HasPrefix(value, "GOTOOLCHAIN=") ||
			strings.HasPrefix(value, "GOROOT=") {
			continue
		}
		filtered = append(filtered, value)
	}
	return append(filtered, "GOWORK=off", "GOTOOLCHAIN=local")
}

func assertCheckpointCompatLegacyReaderRejectsValue(t *testing.T, readerBin string,
	value any, registeredName string) {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(&checkpointCompatAnyEnvelope{Value: value}))
	path := filepath.Join(t.TempDir(), "value.gob")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
	cmd := exec.Command(readerBin, "-gob-any-file", path)
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "name not registered for interface")
	require.Contains(t, string(output), registeredName)
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

func TestCheckpointGobSchemaEvolution(t *testing.T) {
	encode := func(t *testing.T, value any) []byte {
		t.Helper()
		var buf bytes.Buffer
		require.NoError(t, gob.NewEncoder(&buf).Encode(value))
		return buf.Bytes()
	}

	t.Run("added_and_removed_fields_are_compatible", func(t *testing.T) {
		oldBytes := encode(t, &checkpointGobSchemaOld{Stable: "stable", Removed: "legacy"})
		var newer checkpointGobSchemaNew
		require.NoError(t, gob.NewDecoder(bytes.NewReader(oldBytes)).Decode(&newer))
		require.Equal(t, "stable", newer.Stable)
		require.Empty(t, newer.Added)

		newBytes := encode(t, &checkpointGobSchemaNew{Stable: "stable", Added: "new"})
		var older checkpointGobSchemaOld
		require.NoError(t, gob.NewDecoder(bytes.NewReader(newBytes)).Decode(&older))
		require.Equal(t, "stable", older.Stable)
		require.Empty(t, older.Removed)
	})

	t.Run("changing_an_existing_field_type_fails", func(t *testing.T) {
		data := encode(t, &checkpointGobSchemaString{Value: "legacy"})
		var incompatible checkpointGobSchemaStruct
		err := gob.NewDecoder(bytes.NewReader(data)).Decode(&incompatible)
		require.ErrorContains(t, err, "type mismatch")
	})
}
