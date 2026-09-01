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

type fixture struct {
	Name              string   `json:"name"`
	File              string   `json:"file"`
	Depth             int      `json:"depth"`
	ParallelChildren  int      `json:"parallel_children,omitempty"`
	Streaming         bool     `json:"streaming,omitempty"`
	Cancel            bool     `json:"cancel,omitempty"`
	ResumeTargetCount int      `json:"resume_target_count,omitempty"`
	PayloadField      string   `json:"payload_field"`
	PayloadSize       int      `json:"payload_size"`
	InterruptIDs      []string `json:"interrupt_ids"`
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
	name string
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
	isTarget, _, _ := componenttool.GetResumeContext[string](ctx)
	if isTarget {
		return "resumed", nil
	}
	if !hasState {
		return "", fmt.Errorf("legacy reader lost interrupt state for %s", t.name)
	}
	return "", componenttool.StatefulInterrupt(ctx, t.name, "re-interrupted")
}

func newAgent(depth, parallelChildren int) (adk.Agent, error) {
	if parallelChildren > 0 {
		tools := make([]componenttool.BaseTool, 0, parallelChildren)
		names := make([]string, 0, parallelChildren)
		for i := 0; i < parallelChildren; i++ {
			name := fmt.Sprintf("ParallelChild%d", i)
			child, err := newNestedAgent(name, 0)
			if err != nil {
				return nil, err
			}
			tools = append(tools, adk.NewAgentTool(context.Background(), child))
			names = append(names, name)
		}
		return newChatModelAgent("ParallelParent", names, tools)
	}
	return newNestedAgent("RootAgent", depth)
}

func newCancelResumeAgent() (adk.Agent, error) {
	const toolName = "CancelCompletionTool"
	return newChatModelAgent("CancelAgent", []string{toolName},
		[]componenttool.BaseTool{&completionTool{name: toolName}})
}

func newNestedAgent(name string, depth int) (adk.Agent, error) {
	if depth == 0 {
		toolName := name + "Interrupt"
		return newChatModelAgent(name, []string{toolName},
			[]componenttool.BaseTool{&interruptTool{name: toolName}})
	}
	childName := fmt.Sprintf("%sChild%d", name, depth)
	child, err := newNestedAgent(childName, depth-1)
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

func run() error {
	dir := flag.String("fixture-dir", "", "checkpoint fixture directory")
	name := flag.String("fixture", "", "fixture name")
	gobAnyFile := flag.String("gob-any-file", "", "gob-encoded any envelope")
	flag.Parse()
	if *gobAnyFile != "" {
		data, err := os.ReadFile(*gobAnyFile)
		if err != nil {
			return err
		}
		var envelope anyEnvelope
		return gob.NewDecoder(bytes.NewReader(data)).Decode(&envelope)
	}
	if *dir == "" || *name == "" {
		return fmt.Errorf("-fixture-dir and -fixture are required")
	}

	data, err := os.ReadFile(filepath.Join(*dir, "manifest.json"))
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
	raw, err := readFixture(filepath.Join(*dir, selected.File))
	if err != nil {
		return err
	}
	var agent adk.Agent
	if selected.Cancel {
		agent, err = newCancelResumeAgent()
	} else {
		agent, err = newAgent(selected.Depth, selected.ParallelChildren)
	}
	if err != nil {
		return err
	}
	s := &store{data: map[string][]byte{selected.Name: raw}}
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: selected.Streaming,
		CheckPointStore: s,
	})
	targetCount := selected.ResumeTargetCount
	if targetCount == 0 && !selected.Cancel {
		targetCount = len(selected.InterruptIDs)
	}
	targets := make(map[string]any, targetCount)
	for _, id := range selected.InterruptIDs[:targetCount] {
		targets[id] = "resumed"
	}
	iter, err := runner.ResumeWithParams(context.Background(), selected.Name,
		&adk.ResumeParams{Targets: targets})
	if err != nil {
		return err
	}
	var errs []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errs = append(errs, event.Err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("resume events failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
