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

// tool_agent.go implements the Agent tool, which spawns foreground or
// background teammate agents with mailbox-based communication.

package team

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type agentToolArgs struct {
	Name            string `json:"name"`
	Prompt          string `json:"prompt"`
	Description     string `json:"description,omitempty"`
	SubagentType    string `json:"subagent_type,omitempty"`
	TeamName        string `json:"team_name,omitempty"`
	RunInBackground bool   `json:"run_in_background,omitempty"`
}

type agentTool struct {
	mw *teamMiddleware
}

func newAgentTool(mw *teamMiddleware) *agentTool {
	return &agentTool{mw: mw}
}

func (t *agentTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: agentToolName,
		Desc: selectToolDesc(agentToolDesc, agentToolDescChinese),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"name": {
				Type: schema.String,
				Desc: "Name for the spawned agent. Makes it addressable via SendMessage({to: name}) while running.",
			},
			"prompt": {
				Type:     schema.String,
				Desc:     "The task for the agent to perform",
				Required: true,
			},
			"description": {
				Type:     schema.String,
				Desc:     "A short (3-5 word) description of the task",
				Required: true,
			},
			"subagent_type": {
				Type: schema.String,
				Desc: "The type of specialized agent to use for this task",
			},
			"team_name": {
				Type: schema.String,
				Desc: "Optional. Must match the currently active team. A non-matching value is ignored and the active team is used; if there is no active team the call fails.",
			},
			"run_in_background": {
				Type: schema.Boolean,
				Desc: "Set to true to run this agent in the background; you will be notified when it completes. Note: when a team is active and a name is provided, the agent is always run in the background (so it stays addressable via SendMessage) regardless of this flag.",
			},
		}),
	}, nil
}

// InvokableRun dispatches the Agent tool call to either a background teammate or
// a one-shot foreground sub-agent. A teammate is used when either:
//
//   - run_in_background is explicitly requested, or
//   - a team is active AND the caller named the agent. In team mode a named agent
//     is addressable via SendMessage, so it is always spawned in the background
//     regardless of run_in_background. This implicit override is documented in the
//     run_in_background tool schema.
//
// The team-active half of the second predicate is resolved under teamOpLock so it
// is serialized against TeamCreate/TeamDelete: tool calls in one assistant turn
// may run in parallel (see compose tool_node parallelRunToolCall), so reading the
// active team outside the lock could observe an empty team while a concurrent
// TeamCreate is mid-flight and silently downgrade the call to a one-shot
// foreground sub-agent (no team/plantask middleware, not addressable via
// SendMessage).
func (t *agentTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args agentToolArgs
	if err := sonic.UnmarshalString(argumentsInJSON, &args); err != nil {
		return "", fmt.Errorf("parse Agent args: %w", err)
	}

	if args.Prompt == "" || args.Description == "" {
		return "", fmt.Errorf("prompt and description are required")
	}

	// An explicit run_in_background request is always a teammate spawn (it
	// hard-requires an active team, validated inside runTeammate).
	if args.RunInBackground {
		return t.runTeammate(ctx, args)
	}

	// A named agent becomes an addressable background teammate *when a team is
	// active* (see the dispatch policy on InvokableRun). When the team is active we
	// run the teammate body while still holding the lock (mirroring runTeammate);
	// otherwise we release the lock and fall through to the foreground path so a
	// full synchronous sub-agent run never serializes against unrelated
	// team-lifecycle operations.
	if args.Name != "" {
		t.mw.teamOpLock.Lock()
		if t.mw.getTeamName() != "" {
			defer t.mw.teamOpLock.Unlock()
			return t.runTeammateLocked(ctx, args)
		}
		t.mw.teamOpLock.Unlock()
	}

	return t.runForeground(ctx, args)
}

// runForeground runs the agent synchronously by reusing adk.NewAgentTool,
// which handles event iteration, streaming, and interrupt/resume internally.
//
// The foreground agent is a one-shot, isolated sub-agent. It is built directly
// from a shallow copy of agentConfig() and does NOT go through buildTeamAgent,
// so the team layer injects none of its own middleware: no team-aware plantask
// middleware and no team middleware. It therefore cannot see the shared task
// list, is not addressable via SendMessage, and cannot spawn teammates. Use a
// background teammate (named, or run_in_background=true) when those
// capabilities are needed.
//
// This withholds team-injected middleware, not the caller's own
// AgentConfig.Handlers: any handlers the user attached to AgentConfig are
// inherited as-is (the shallow copy shares the Handlers slice header). The team
// layer never adds a plantask middleware on this path, but if the user supplied
// one of their own it still runs. Foreground isolation is about withholding team
// capabilities, not about scrubbing the user's handler chain.
func (t *agentTool) runForeground(ctx context.Context, args agentToolArgs) (string, error) {
	newConfig := *t.mw.lifecycle.agentConfig()
	newConfig.Instruction = args.Prompt

	agent, err := adk.NewChatModelAgent(ctx, &newConfig)
	if err != nil {
		return "", fmt.Errorf("create agent: %w", err)
	}

	agentToolInstance := adk.NewAgentTool(ctx, agent)
	invokable, ok := agentToolInstance.(tool.InvokableTool)
	if !ok {
		return "", fmt.Errorf("agent tool does not implement InvokableTool")
	}

	requestJSON, err := sonic.MarshalString(map[string]string{"request": args.Prompt})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	return invokable.InvokableRun(ctx, requestJSON)
}

// runTeammate spawns the agent as a background teammate with mailbox-based communication.
// It requires team mode (a team must have been created via TeamCreate first); without an
// active team context the call returns errTeamNotFound.
//
// This entry point acquires teamOpLock itself. It is used by the explicit
// run_in_background path, where InvokableRun has not already taken the lock. The
// named-agent path resolves the team-active decision under the lock and then
// calls runTeammateLocked directly to avoid re-acquiring (and deadlocking on) the
// non-reentrant teamOpLock.
func (t *agentTool) runTeammate(ctx context.Context, args agentToolArgs) (string, error) {
	// Serialize the whole "read active team name → register member → spawn
	// teammate" sequence against the other leader-only lifecycle tools
	// (TeamCreate, TeamDelete). Tool calls in one assistant turn may run in
	// parallel, so without this lock an Agent spawn could interleave with a
	// TeamDelete that already saw an empty teammate registry and is tearing the
	// team directories down — leaving a registered member and a running goroutine
	// bound to a team that no longer exists on disk.
	t.mw.teamOpLock.Lock()
	defer t.mw.teamOpLock.Unlock()
	return t.runTeammateLocked(ctx, args)
}

// runTeammateLocked performs the teammate spawn. The caller MUST already hold
// t.mw.teamOpLock for the full duration of the call.
func (t *agentTool) runTeammateLocked(ctx context.Context, args agentToolArgs) (string, error) {
	if args.Name == "" {
		args.Name = defaultTeammateName
	}
	if err := validateMemberName(args.Name); err != nil {
		return "", err
	}

	// The active team is whatever TeamCreate established on this leader middleware.
	// We deliberately do NOT fall back to args.TeamName when no team is active: a
	// teammate spawn must attach to the team the leader is actually running (its
	// inbox pump, router, and plantask state are all bound to that team). Honoring
	// an arbitrary args.TeamName here would register a member and start a teammate
	// against a team the leader never activated — its messages to the leader would
	// land in an inbox no pump is reading. A non-matching team_name is logged and
	// ignored; an empty active team is a hard error.
	teamName := t.mw.getTeamName()
	if args.TeamName != "" && teamName != args.TeamName {
		t.mw.logger().Printf("[AgentTool] team_name %q is not the active team %q; using the active team\n", args.TeamName, teamName)
	}
	if teamName == "" {
		return "", fmt.Errorf("run_in_background requires an active team (create one with TeamCreate first): %w", errTeamNotFound)
	}

	member, err := t.registerTeammate(ctx, teamName, &args)
	if err != nil {
		return "", err
	}

	// From this point on, any failure must clean up the registered member.
	// Use defer+flag so cleanup is never accidentally skipped.
	succeeded := false
	defer func() {
		if !succeeded {
			// Use a background context with timeout for cleanup because the tool
			// call's ctx may already be cancelled (e.g., user cancellation, timeout).
			// This mirrors the pattern in cleanupExitedTeammate.
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
			defer cleanupCancel()
			t.mw.lifecycle.cleanupFailedTeammateSpawn(cleanupCtx, teamName, args.Name)
		}
	}()

	if sendErr := t.sendInitialPrompt(ctx, teamName, args); sendErr != nil {
		return "", sendErr
	}

	tmAgent, err := t.buildTeammateAgent(ctx, teamName, args)
	if err != nil {
		return "", err
	}

	if err := t.spawnTeammateRunner(ctx, teamName, args.Name, tmAgent); err != nil {
		return "", err
	}

	succeeded = true

	var sb strings.Builder
	sb.WriteString("Spawned successfully.\nagent_id: ")
	sb.WriteString(member.AgentID)
	sb.WriteString("\nname: ")
	sb.WriteString(args.Name)
	sb.WriteString("\nteam_name: ")
	sb.WriteString(teamName)
	sb.WriteString("\nThe agent is now running and will receive instructions via mailbox.")
	return sb.String(), nil
}

// registerTeammate registers the teammate in the team config with a deduplicated name.
func (t *agentTool) registerTeammate(ctx context.Context, teamName string, args *agentToolArgs) (teamMember, error) {
	member, err := t.mw.lifecycle.addTeammateMember(ctx, teamName, teamMember{
		Name:      args.Name,
		AgentType: args.SubagentType,
		Prompt:    args.Prompt,
		JoinedAt:  time.Now(),
	})
	if err != nil {
		return teamMember{}, fmt.Errorf("register teammate: %w", err)
	}
	args.Name = member.Name
	return member, nil
}

// sendInitialPrompt creates the teammate's inbox and sends the initial prompt message.
func (t *agentTool) sendInitialPrompt(ctx context.Context, teamName string, args agentToolArgs) error {
	if initErr := t.mw.lifecycle.initInbox(ctx, teamName, args.Name); initErr != nil {
		return fmt.Errorf("create inbox file: %w", initErr)
	}

	mb := t.mw.lifecycle.mailbox(teamName, LeaderAgentName)
	if sendErr := mb.Send(ctx, &outboxMessage{
		To:      args.Name,
		Type:    messageTypeDM,
		Text:    args.Prompt,
		Summary: args.Description,
	}); sendErr != nil {
		return fmt.Errorf("send initial prompt to teammate: %w", sendErr)
	}
	return nil
}

// buildTeammateAgent constructs the agent with team and plantask middleware wired up.
func (t *agentTool) buildTeammateAgent(ctx context.Context, teamName string, args agentToolArgs) (*adk.ChatModelAgent, error) {
	return t.mw.lifecycle.buildTeammateAgent(ctx, args.Name, teamName)
}

// spawnTeammateRunner creates the teammate's TurnLoop runner and starts it in a goroutine.
//
// The teammate's runtime context is derived from the team runtime root context
// (captured when the Runner started), NOT from the tool call's ctx. The tool
// ctx can be a short-lived per-turn context — e.g. when the host returns a
// per-turn GenInputResult.RunCtx with its own deadline/cancel — and binding a
// background teammate to it would cancel the teammate the moment the spawning
// turn ends, breaking the "background teammate survives across turns" contract.
// The teammate is instead torn down explicitly (TeamDelete / shutdown_request /
// Runner shutdown) via the Cancel func registered below.
func (t *agentTool) spawnTeammateRunner(ctx context.Context, teamName, name string, tmAgent *adk.ChatModelAgent) error {
	rootCtx := t.mw.lifecycle.teammateRootContext(ctx)
	appCtx, cancel := context.WithCancel(rootCtx)
	runner, err := t.mw.lifecycle.createTeammateRunner(tmAgent, name, teamName)
	if err != nil {
		cancel()
		return fmt.Errorf("create teammate runner: %w", err)
	}

	t.mw.lifecycle.startTeammateRunner(appCtx, teamName, name, &teammateHandle{
		Cancel: cancel,
	}, func(ctx context.Context) error {
		// Start the mailbox pump before Run so that the initial prompt (already
		// written to the inbox file by sendInitialPrompt) is picked up and pushed
		// into the TurnLoop's buffer immediately. TurnLoop.Push works before Run
		// (items are buffered), so this ordering is safe and avoids a window where
		// the loop is running but has no items to consume.
		t.mw.lifecycle.startPump(ctx, name)
		runner.Run(ctx)
		exitState := runner.Wait()
		if exitState != nil && exitState.ExitReason != nil {
			return exitState.ExitReason
		}
		return nil
	})

	return nil
}
