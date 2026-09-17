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

// Package main verifies that the pinned legacy Eino release can resume frozen checkpoints.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type manifest struct {
	Fixtures []fixture `json:"fixtures"`
}

type anyEnvelope struct {
	Value any
}

const (
	resumeMethodImplicit = "resume"
	resumeMethodTargeted = "resume_with_params"
)

type fixture struct {
	Name               string   `json:"name"`
	File               string   `json:"file"`
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
}

type resumeOutcome struct {
	TerminalOutputs             int      `json:"terminal_outputs"`
	RemainingInterruptAddresses []string `json:"remaining_interrupt_addresses"`
	ResumeMethod                string   `json:"resume_method"`
}

type store struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *store) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = append([]byte(nil), value...)
	return nil
}

func (s *store) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[key]
	return append([]byte(nil), value...), ok, nil
}

type chatModel struct {
	toolNames []string
}

func (m *chatModel) Generate(_ context.Context, input []*schema.Message,
	_ ...model.Option) (*schema.Message, error) {
	return m.response(input), nil
}

func (m *chatModel) Stream(_ context.Context, input []*schema.Message,
	_ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.response(input)}), nil
}

func (m *chatModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *chatModel) response(input []*schema.Message) *schema.Message {
	if len(input) > 0 && input[len(input)-1].Role == schema.Tool {
		return schema.AssistantMessage("completed", nil)
	}
	calls := make([]schema.ToolCall, 0, len(m.toolNames))
	for i, name := range m.toolNames {
		calls = append(calls, schema.ToolCall{
			ID: fmt.Sprintf("call-%d", i),
			Function: schema.FunctionCall{
				Name:      name,
				Arguments: `{"request":"continue"}`,
			},
		})
	}
	return schema.AssistantMessage("", calls)
}

type interruptTool struct {
	name           string
	implicitResume bool
}

type completionTool struct {
	name string
}

func (t *completionTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "legacy cancel completion"}, nil
}

func (t *completionTool) InvokableRun(context.Context, string,
	...componenttool.Option) (string, error) {
	return "completed tool", nil
}

func (t *interruptTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "legacy checkpoint reader interrupt"}, nil
}

func (t *interruptTool) InvokableRun(ctx context.Context, _ string,
	_ ...componenttool.Option) (string, error) {
	wasInterrupted, hasState, _ := componenttool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return "", componenttool.StatefulInterrupt(ctx, t.name, "interrupted")
	}
	if !hasState {
		return "", fmt.Errorf("legacy reader lost interrupt state for %s", t.name)
	}
	if t.implicitResume {
		return "resumed", nil
	}
	isTarget, _, _ := componenttool.GetResumeContext[string](ctx)
	if isTarget {
		return "resumed", nil
	}
	return "", componenttool.StatefulInterrupt(ctx, t.name, "re-interrupted")
}

func newAgent(depth, parallelChildren int, implicitResume bool) (adk.Agent, error) {
	if parallelChildren > 0 {
		tools := make([]componenttool.BaseTool, 0, parallelChildren)
		names := make([]string, 0, parallelChildren)
		for i := 0; i < parallelChildren; i++ {
			name := fmt.Sprintf("ParallelChild%d", i)
			child, err := newNestedAgent(name, 0, implicitResume)
			if err != nil {
				return nil, err
			}
			tools = append(tools, adk.NewAgentTool(context.Background(), child))
			names = append(names, name)
		}
		return newChatModelAgent("ParallelParent", names, tools)
	}
	return newNestedAgent("RootAgent", depth, implicitResume)
}

func newCancelResumeAgent() (adk.Agent, error) {
	const toolName = "CancelCompletionTool"
	return newChatModelAgent("CancelAgent", []string{toolName},
		[]componenttool.BaseTool{&completionTool{name: toolName}})
}

func newNestedAgent(name string, depth int, implicitResume bool) (adk.Agent, error) {
	if depth == 0 {
		toolName := name + "Interrupt"
		return newChatModelAgent(name, []string{toolName},
			[]componenttool.BaseTool{&interruptTool{
				name:           toolName,
				implicitResume: implicitResume,
			}})
	}
	childName := fmt.Sprintf("%sChild%d", name, depth)
	child, err := newNestedAgent(childName, depth-1, implicitResume)
	if err != nil {
		return nil, err
	}
	return newChatModelAgent(name, []string{childName},
		[]componenttool.BaseTool{adk.NewAgentTool(context.Background(), child)})
}

func newChatModelAgent(name string, toolNames []string,
	tools []componenttool.BaseTool) (adk.Agent, error) {
	return adk.NewChatModelAgent(context.Background(), &adk.ChatModelAgentConfig{
		Name:        name,
		Description: "legacy checkpoint reader agent",
		Model:       &chatModel{toolNames: toolNames},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools},
		},
	})
}

func readFixture(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

type checkpointResumer interface {
	Resume(context.Context, string, ...adk.AgentRunOption) (
		*adk.AsyncIterator[*adk.AgentEvent], error)
	ResumeWithParams(context.Context, string, *adk.ResumeParams, ...adk.AgentRunOption) (
		*adk.AsyncIterator[*adk.AgentEvent], error)
}

func validateFixture(selected fixture) (int, error) {
	if selected.Depth < 0 {
		return 0, fmt.Errorf("fixture %q has invalid depth %d", selected.Name, selected.Depth)
	}
	if selected.ParallelChildren < 0 {
		return 0, fmt.Errorf("fixture %q has invalid parallel children %d",
			selected.Name, selected.ParallelChildren)
	}
	if selected.ResumeTargetCount < 0 {
		return 0, fmt.Errorf("fixture %q has invalid resume target count %d",
			selected.Name, selected.ResumeTargetCount)
	}
	targetCount := selected.ResumeTargetCount
	if targetCount == 0 && !selected.Cancel {
		targetCount = len(selected.InterruptIDs)
	}
	if targetCount > len(selected.InterruptIDs) {
		return 0, fmt.Errorf("fixture %q resume target count %d exceeds interrupt IDs length %d",
			selected.Name, targetCount, len(selected.InterruptIDs))
	}
	if targetCount > len(selected.InterruptAddresses) {
		return 0, fmt.Errorf(
			"fixture %q resume target count %d exceeds interrupt addresses length %d",
			selected.Name, targetCount, len(selected.InterruptAddresses))
	}
	if selected.ExpectedInterrupts < 0 {
		return 0, fmt.Errorf("fixture %q has invalid expected interrupts %d",
			selected.Name, selected.ExpectedInterrupts)
	}
	remainingInterrupts := len(selected.InterruptAddresses) - targetCount
	if remainingInterrupts != selected.ExpectedInterrupts {
		return 0, fmt.Errorf(
			"fixture %q metadata declares %d remaining interrupts, but has %d addresses",
			selected.Name, selected.ExpectedInterrupts, remainingInterrupts)
	}
	return targetCount, nil
}

func resumeFixture(ctx context.Context, runner checkpointResumer, selected fixture, targetCount int) (
	iter *adk.AsyncIterator[*adk.AgentEvent], method string, err error) {
	if selected.ImplicitResume {
		iter, err = runner.Resume(ctx, selected.Name)
		return iter, resumeMethodImplicit, err
	}
	targets := make(map[string]any, targetCount)
	for _, id := range selected.InterruptIDs[:targetCount] {
		targets[id] = "resumed"
	}
	iter, err = runner.ResumeWithParams(ctx, selected.Name, &adk.ResumeParams{Targets: targets})
	return iter, resumeMethodTargeted, err
}

type runDependencies struct {
	readFile    func(string) ([]byte, error)
	readFixture func(string) ([]byte, error)
	newResumer  func(context.Context, fixture, []byte) (checkpointResumer, error)
}

func defaultRunDependencies() runDependencies {
	return runDependencies{
		readFile:    os.ReadFile,
		readFixture: readFixture,
		newResumer: func(ctx context.Context, selected fixture,
			raw []byte) (checkpointResumer, error) {
			var (
				agent adk.Agent
				err   error
			)
			if selected.Cancel {
				agent, err = newCancelResumeAgent()
			} else {
				agent, err = newAgent(
					selected.Depth, selected.ParallelChildren, selected.ImplicitResume)
			}
			if err != nil {
				return nil, err
			}
			s := &store{data: map[string][]byte{selected.Name: raw}}
			return adk.NewRunner(ctx, adk.RunnerConfig{
				Agent:           agent,
				EnableStreaming: selected.Streaming,
				CheckPointStore: s,
			}), nil
		},
	}
}

func run(args []string, stdout io.Writer, dependencies runDependencies) error {
	flags := flag.NewFlagSet("checkpoint-legacy-reader", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("fixture-dir", "", "checkpoint fixture directory")
	name := flags.String("fixture", "", "fixture name")
	gobAnyFile := flags.String("gob-any-file", "", "gob-encoded any envelope")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *gobAnyFile != "" {
		data, err := dependencies.readFile(*gobAnyFile)
		if err != nil {
			return err
		}
		var envelope anyEnvelope
		return gob.NewDecoder(bytes.NewReader(data)).Decode(&envelope)
	}
	if *dir == "" || *name == "" {
		return fmt.Errorf("-fixture-dir and -fixture are required")
	}

	data, err := dependencies.readFile(filepath.Join(*dir, "manifest.json"))
	if err != nil {
		return err
	}
	var m manifest
	if err = json.Unmarshal(data, &m); err != nil {
		return err
	}
	var selected *fixture
	for i := range m.Fixtures {
		if m.Fixtures[i].Name == *name {
			selected = &m.Fixtures[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("fixture %q not found", *name)
	}
	targetCount, err := validateFixture(*selected)
	if err != nil {
		return err
	}
	raw, err := dependencies.readFixture(filepath.Join(*dir, selected.File))
	if err != nil {
		return err
	}
	ctx := context.Background()
	runner, err := dependencies.newResumer(ctx, *selected, raw)
	if err != nil {
		return err
	}
	iter, resumeMethod, err := resumeFixture(ctx, runner, *selected, targetCount)
	if err != nil {
		return err
	}
	var errs []string
	var eventCount int
	outcome := resumeOutcome{ResumeMethod: resumeMethod}
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		eventCount++
		if event.Err != nil {
			errs = append(errs, event.Err.Error())
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg, msgErr := event.Output.MessageOutput.GetMessage()
			if msgErr != nil {
				errs = append(errs, msgErr.Error())
			} else if msg != nil && msg.Role == schema.Assistant && msg.Content == "completed" {
				outcome.TerminalOutputs++
			}
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
				outcome.RemainingInterruptAddresses = append(
					outcome.RemainingInterruptAddresses, interruptCtx.Address.String())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("resume events failed: %s", strings.Join(errs, "; "))
	}
	if eventCount == 0 {
		return fmt.Errorf("fixture %q resume produced no events", selected.Name)
	}
	expectedAddresses := append([]string(nil), selected.InterruptAddresses[targetCount:]...)
	sort.Strings(expectedAddresses)
	sort.Strings(outcome.RemainingInterruptAddresses)
	if !reflect.DeepEqual(outcome.RemainingInterruptAddresses, expectedAddresses) {
		return fmt.Errorf("fixture %q remaining interrupt addresses = %q, want %q",
			selected.Name, outcome.RemainingInterruptAddresses, expectedAddresses)
	}
	if selected.ExpectedInterrupts == 0 {
		if outcome.TerminalOutputs != 1 {
			return fmt.Errorf("fixture %q terminal outputs = %d, want 1",
				selected.Name, outcome.TerminalOutputs)
		}
	} else if outcome.TerminalOutputs != 0 {
		return fmt.Errorf("partial fixture %q terminal outputs = %d, want 0",
			selected.Name, outcome.TerminalOutputs)
	}
	return json.NewEncoder(stdout).Encode(&outcome)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, defaultRunDependencies()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
