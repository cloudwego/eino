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

// Package main demonstrates the Agent Teams middleware for multi-agent collaboration.
//
// This demo creates a Leader agent that spawns two Teammate agents (researcher and coder).
// The Leader coordinates the teammates via the mailbox messaging system, using an
// in-memory backend for storage.
//
// Architecture:
//
//	Leader (team-lead)
//	  ├── TeamCreate tool  → creates team + config.json
//	  ├── Agent tool       → spawns teammates
//	  ├── SendMessage tool → DM / broadcast
//	  └── TeamDelete tool  → cleanup
//
//	Teammate (researcher / coder)
//	  └── SendMessage tool → reply to leader / peers
//
// Flow:
//  1. Leader creates a team
//  2. Leader spawns teammates via Agent tool
//  3. Teammates receive initial prompt, do work, send results back
//  4. Leader collects results, sends shutdown, then deletes team
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/team"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func main() {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/data"

	teamConf := &team.Config{
		Backend: backend,
		BaseDir: baseDir,
	}

	// plantask middleware is now automatically included by team.NewRunner,
	// providing TaskCreate/TaskGet/TaskUpdate/TaskList tools and task
	// assignment notifications out of the box.

	// Create a channel-based source and send the initial user prompt.
	source := team.NewChanSource(1)
	source.Send(team.TurnInput{
		TargetAgent: team.LeaderAgentName,
		Messages:    []string{"Build a web scraper in Go using colly."},
	})

	runner, err := team.NewRunner(ctx, &team.RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{
			Name:        "team-lead",
			Description: "Leader that coordinates a research team",
			Instruction: "You are the team leader. Create a team, spawn teammates, coordinate their work, and collect results.",
			Model:       newFakeModel("team-lead"),
		},
		TeamConfig: teamConf,
		Source:     source,
		GenInput: func(_ context.Context, input team.TurnInput) (*adk.AgentInput, []adk.AgentRunOption, error) {
			return &adk.AgentInput{
				Messages: inboxToMessages(input.Messages),
			}, nil, nil
		},
		OnAgentEvents: func(_ context.Context, inputItem team.TurnInput, events *adk.AsyncIterator[*adk.AgentEvent]) error {
			for {
				event, ok := events.Next()
				if !ok {
					break
				}
				if event.Err != nil {
					return event.Err
				}
				if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
					fmt.Printf("[team-lead/%s] assistant: %s\n", inputItem.TargetAgent, event.Output.MessageOutput.Message.Content)
				}
			}
			return nil
		},
	})
	if err != nil {
		log.Fatalf("create leader runner: %v", err)
	}

	fmt.Println("=== Agent Teams Demo ===")
	fmt.Println()

	if err := runner.Run(ctx); err != nil {
		log.Fatalf("leader runner error: %v", err)
	}

	fmt.Println()
	fmt.Println("=== Demo Complete ===")
}

// ---------------------------------------------------------------------------
// Helper: convert inbox messages to schema messages
// ---------------------------------------------------------------------------

func inboxToMessages(msgs []string) []*schema.Message {
	if len(msgs) == 0 {
		return nil
	}
	var sb strings.Builder
	for _, msg := range msgs {
		sb.WriteString(msg)
		sb.WriteString("\n")
	}
	return []*schema.Message{schema.UserMessage(sb.String())}
}

// ---------------------------------------------------------------------------
// Fake model for demonstration (replaces real LLM calls)
// ---------------------------------------------------------------------------

// fakeModel simulates a ChatModel that returns scripted responses with tool calls.
// In a real application, replace this with an actual LLM like openai.NewChatModel().
type fakeModel struct {
	name  string
	calls int
	mu    sync.Mutex
}

func newFakeModel(name string) model.BaseChatModel {
	return &fakeModel{name: name}
}

func (m *fakeModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()

	lastContent := ""
	if len(input) > 0 {
		lastContent = input[len(input)-1].Content
	}

	switch m.name {
	case "team-lead":
		return m.leaderResponse(call, lastContent)
	default:
		return m.teammateResponse(call, lastContent)
	}
}

func (m *fakeModel) leaderResponse(call int, _ string) (*schema.Message, error) {
	switch call {
	case 1:
		// First call: create a team
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "tc1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "TeamCreate",
					Arguments: `{"team_name":"web-scraper-team","description":"Team to build a web scraper"}`,
				},
			},
		}), nil
	case 2:
		// Second call: spawn researcher
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "tc2",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Agent",
					Arguments: `{"name":"researcher","prompt":"Research the best Go web scraping libraries. Compare colly, goquery, and rod. Send your findings back to team-lead.","description":"Research agent for web scraping libraries"}`,
				},
			},
		}), nil
	case 3:
		// Third call: spawn coder
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "tc3",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Agent",
					Arguments: `{"name":"coder","prompt":"Write a simple Go web scraper using colly. Send the code back to team-lead.","description":"Coding agent for implementation"}`,
				},
			},
		}), nil
	case 4:
		// Fourth call: acknowledge results, send shutdown to researcher
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "tc4",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "SendMessage",
					Arguments: `{"type":"shutdown_request","recipient":"researcher","content":"work complete"}`,
				},
			},
		}), nil
	case 5:
		// Send shutdown to coder
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "tc5",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "SendMessage",
					Arguments: `{"type":"shutdown_request","recipient":"coder","content":"work complete"}`,
				},
			},
		}), nil
	case 6:
		// Delete team
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "tc6",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "TeamDelete",
					Arguments: `{}`,
				},
			},
		}), nil
	default:
		return schema.AssistantMessage("All done! The web scraper team has completed its work.", nil), nil
	}
}

func (m *fakeModel) teammateResponse(call int, lastContent string) (*schema.Message, error) {
	_ = lastContent
	switch call {
	case 1:
		// Teammate does its work and sends result back to leader
		var content string
		if strings.Contains(m.name, "researcher") {
			content = "Research complete: colly is best for structured scraping, goquery for HTML parsing, rod for JS-heavy sites. Recommend colly for this task."
		} else {
			content = "Code complete: implemented a web scraper using colly that crawls a target URL and extracts links and titles. See attached code."
		}
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:   "ttc1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "SendMessage",
					Arguments: fmt.Sprintf(`{"type":"message","recipient":"team-lead","content":"%s","summary":"Task result"}`, content),
				},
			},
		}), nil
	default:
		// Subsequent calls: just acknowledge
		return schema.AssistantMessage("Task acknowledged.", nil), nil
	}
}

func (m *fakeModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (
	*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("streaming not supported in fake model")
}

// ---------------------------------------------------------------------------
// In-memory Backend (implements team.Backend / plantask.Backend)
// ---------------------------------------------------------------------------

type inMemoryBackend struct {
	files map[string]string
	mu    sync.RWMutex
}

func newInMemoryBackend() *inMemoryBackend {
	return &inMemoryBackend{files: make(map[string]string)}
}

func (b *inMemoryBackend) LsInfo(_ context.Context, req *team.LsInfoRequest) ([]team.FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	reqPath := strings.TrimSuffix(req.Path, "/")
	var result []team.FileInfo
	for path := range b.files {
		dir := filepath.Dir(path)
		if dir == reqPath {
			result = append(result, team.FileInfo{Path: path})
		}
	}
	return result, nil
}

func (b *inMemoryBackend) Read(_ context.Context, req *team.ReadRequest) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	content, ok := b.files[req.FilePath]
	if !ok {
		return "", fmt.Errorf("file not found: %s", req.FilePath)
	}
	return content, nil
}

func (b *inMemoryBackend) Write(_ context.Context, req *team.WriteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.files[req.FilePath] = req.Content
	return nil
}

func (b *inMemoryBackend) Delete(_ context.Context, req *team.DeleteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	prefix := req.FilePath + "/"
	for k := range b.files {
		if k == req.FilePath || strings.HasPrefix(k, prefix) {
			delete(b.files, k)
		}
	}
	return nil
}

func (b *inMemoryBackend) Exists(_ context.Context, path string) (bool, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	_, ok := b.files[path]
	return ok, nil
}

func (b *inMemoryBackend) Mkdir(_ context.Context, _ string) error {
	return nil
}

func (b *inMemoryBackend) FileExists(_ context.Context, path string) (bool, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	_, ok := b.files[path]
	return ok, nil
}
