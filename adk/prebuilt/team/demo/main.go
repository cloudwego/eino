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
// This demo creates a Leader agent that spawns three Teammate agents (security-reviewer,
// perf-reviewer, test-reviewer) to review a PR. The flow closely follows the real
// Claude Code Agent Teams protocol, using an in-memory backend for storage.
//
// Architecture:
//
//	Leader (team-lead)
//	  ├── TaskCreate tool  → creates review tasks
//	  ├── Agent tool       → spawns teammates (run_in_background)
//	  └── SendMessage tool → shutdown_request to teammates
//
//	Teammate (security-reviewer / perf-reviewer / test-reviewer)
//	  ├── TaskUpdate tool  → claim and complete tasks
//	  └── SendMessage tool → send report to leader / shutdown_approved
//
// Flow:
//  1. The team is created automatically when the Runner is constructed.
//  2. Leader creates 3 review tasks
//  3. Leader spawns 3 teammates via Agent tool (run_in_background=true)
//  4. Each teammate: TaskUpdate(in_progress) → TaskUpdate(completed) → SendMessage(report)
//  5. Leader receives reports, sends shutdown_request to each teammate
//  6. Each teammate responds with shutdown_approved
//  7. Leader receives teammate_terminated notifications; the team is cleaned up
//     automatically when the Runner exits.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/adk/prebuilt/team"
	"github.com/cloudwego/eino/schema"
)

type agentHistory struct {
	mu       sync.Mutex
	messages map[string][]*schema.Message
}

func newAgentHistory() *agentHistory {
	return &agentHistory{messages: make(map[string][]*schema.Message)}
}

func (h *agentHistory) append(agent string, msgs ...*schema.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages[agent] = append(h.messages[agent], msgs...)
}

func (h *agentHistory) snapshot(agent string) []*schema.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]*schema.Message, len(h.messages[agent]))
	copy(cp, h.messages[agent])
	return cp
}

func main() {
	ctx := context.Background()

	baseDir := "./base_dir"
	os.RemoveAll(baseDir)
	if _, err := os.Stat(baseDir); err != nil {
		os.MkdirAll(baseDir, 0755)
	}

	backend := newFileBackend(baseDir) // 或留空使用系统临时目录

	leaderPrompt := `
	我有三个问题：
	问题一： 天空为什么是蓝色的？
	问题二： 海水为什么是咸的？
	问题三： 人或者的意义是什么？
	请生成 3 个知识专家队友，分别来回答这三个问题，回答精简一点，一句话就行了。
	teamlead 不要自己回答问题，要等三个知识专家都回答了，再汇总答案给我。
	`

	// modelImpl := newScriptedModel(scenario)
	modelImpl, err := NewChatModel(ctx)
	if err != nil {
		log.Fatalf("create chat model: %v", err)
	}

	history := newAgentHistory()

	runner, err := team.NewRunner(ctx, &team.RunnerConfig{
		AgentConfig: &adk.ChatModelAgentConfig{
			Name:          "team-lead",
			Description:   "Leader that coordinates a research team",
			Instruction:   "You are a helpful assistant.",
			Model:         modelImpl,
			MaxIterations: 1000,

			Handlers: []adk.ChatModelAgentMiddleware{
				NewToolWrapMiddleware(),
			},
		},
		TeamConfig: &team.Config{
			Backend:  backend,
			BaseDir:  baseDir,
			Interval: 10,
		},
		GenInput: func(_ context.Context, _ *adk.TurnLoop[team.TurnInput, adk.Message], items []team.TurnInput) (*adk.GenInputResult[team.TurnInput, adk.Message], error) {
			target := ""
			if len(items) > 0 {
				target = items[0].TargetAgent
			}

			for _, item := range items {
				if item.TargetAgent != target {
					panic("GenInput: items must have the same target agent")
				}
				log.Printf("Received GenInput [%s]: %v", target, item)
				msgs := inboxToMessages(item.Messages)
				history.append(target, msgs...)
			}
			allMessages := history.snapshot(target)

			log.Printf("GenInput [%s]: messages.len : %d ， items.len : %d", target, len(allMessages), len(items))

			return &adk.GenInputResult[team.TurnInput, adk.Message]{
				Input: &adk.AgentInput{
					Messages:        allMessages,
					EnableStreaming: false,
				},
				Consumed: items,
			}, nil
		},
		OnAgentEvents: func(_ context.Context, tc *adk.TurnContext[team.TurnInput, adk.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
			target := ""
			if len(tc.Consumed) == 0 {
				panic("OnAgentEvents: consumed must not be empty")
			}

			target = tc.Consumed[0].TargetAgent

			for {
				msg, ok := events.Next()
				if !ok {
					break
				}
				if msg.Err != nil {
					return msg.Err
				}

				if msg.Output == nil || msg.Output.MessageOutput == nil {
					continue
				}

				mo := msg.Output.MessageOutput
				if mo.Message != nil {
					history.append(target, mo.Message)
				}
				printEvent(msg)
			}

			return nil
		},
	})
	if err != nil {
		log.Fatalf("create leader runner: %v", err)
	}

	runner.Push(team.TurnInput{
		TargetAgent: team.LeaderAgentName,
		Messages:    []string{leaderPrompt},
	})

	go func() {
		time.Sleep(90 * time.Second)
		runner.Push(team.TurnInput{
			TargetAgent: team.LeaderAgentName,
			Messages:    []string{"现在几点？请回答我。"},
		})
		// time.Sleep(30 * time.Second)
		// runner.Push(team.TurnInput{
		// 	TargetAgent: team.LeaderAgentName,
		// 	Messages:    []string{leaderPrompt},
		// })
		// time.Sleep(90 * time.Second)
		// printflag = true
		// runner.Push(team.TurnInput{
		// 	TargetAgent: team.LeaderAgentName,
		// 	Messages:    []string{"1+1=？"},
		// })
	}()

	fmt.Println("=== Agent Teams Demo ===")
	fmt.Println()

	runner.Run(ctx)
	exitState := runner.Wait()
	if exitState != nil && exitState.ExitReason != nil {
		log.Printf("leader runner exit reason: %v", exitState.ExitReason)
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
// File-based Backend that persists data to the filesystem
// ---------------------------------------------------------------------------

type fileBackend struct {
	baseDir       string
	mu            sync.RWMutex
	seededInboxes map[string]bool
}

func newFileBackend(baseDir string) *fileBackend {
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	return &fileBackend{
		baseDir:       baseDir,
		seededInboxes: make(map[string]bool),
	}
}

func (b *fileBackend) LsInfo(_ context.Context, req *team.LsInfoRequest) ([]team.FileInfo, error) {
	entries, err := os.ReadDir(req.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", req.Path, err)
	}

	var result []team.FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result = append(result, team.FileInfo{
			Path:       filepath.Join(req.Path, entry.Name()),
			IsDir:      entry.IsDir(),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (b *fileBackend) Read(_ context.Context, req *team.ReadRequest) (*filesystem.FileContent, error) {
	data, err := os.ReadFile(req.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", req.FilePath)
		}
		return nil, fmt.Errorf("read file %s: %w", req.FilePath, err)
	}
	return &filesystem.FileContent{Content: string(data)}, nil
}

func (b *fileBackend) Write(_ context.Context, req *team.WriteRequest) error {
	dir := filepath.Dir(req.FilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	content := req.Content

	// Atomic replace: write to a temp file in the same directory, fsync, then
	// rename over the target. This satisfies the team.Backend durability contract
	// so a crash mid-write can never leave a truncated config.json or inbox.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(req.FilePath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", req.FilePath, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we bail out before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file for %s: %w", req.FilePath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file for %s: %w", req.FilePath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", req.FilePath, err)
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		return fmt.Errorf("chmod temp file for %s: %w", req.FilePath, err)
	}
	if err := os.Rename(tmpName, req.FilePath); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", req.FilePath, err)
	}
	return nil
}

func (b *fileBackend) Delete(_ context.Context, req *team.DeleteRequest) error {
	info, err := os.Stat(req.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", req.FilePath, err)
	}

	if info.IsDir() {
		if err := os.RemoveAll(req.FilePath); err != nil {
			return fmt.Errorf("remove dir %s: %w", req.FilePath, err)
		}
	} else {
		if err := os.Remove(req.FilePath); err != nil {
			return fmt.Errorf("remove file %s: %w", req.FilePath, err)
		}
	}
	return nil
}

func (b *fileBackend) Exists(_ context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

func (b *fileBackend) Mkdir(_ context.Context, path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	return nil
}

func (b *fileBackend) FileExists(_ context.Context, path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return !info.IsDir(), nil
}
