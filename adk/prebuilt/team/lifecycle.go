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

// lifecycle.go manages teammate spawning, cleanup, and termination notification.
//
// lifecycleManager is the central facade between tool implementations and
// internal infrastructure (registry, config store, router, pump manager,
// plantask). All tool files (tool_agent, tool_team_create, tool_team_delete,
// tool_send_message) access infrastructure exclusively through lifecycle
// methods, never through direct field access. This keeps teamMiddleware
// focused on tool injection (BeforeAgent) and session state.

package team

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

// teammateHandle holds the runtime handle for a spawned teammate:
// its cancel function for cleanup on shutdown.
type teammateHandle struct {
	Cancel context.CancelFunc
}

// lifecycleManager manages teammate creation, cleanup, and termination.
// It bridges the teammateRegistry with Config, plantask middleware,
// and sourceRouter for a complete lifecycle. Extracted from teamMiddleware to
// follow the Single Responsibility Principle.
type lifecycleManager struct {
	registry   *teammateRegistry                                                // tracks active teammate goroutines
	ptMW       plantask.Middleware                                              // plantask middleware for task operations
	router     *sourceRouter                                                    // multi-agent message routing
	pumpMgr    *pumpManager                                                     // mailbox pump goroutine management
	teamCfg    *Config                                                          // team configuration (Backend, BaseDir, etc.)
	runnerConf *RunnerConfig                                                    // full runner config, needed for teammate creation
	isLeader   bool                                                             // whether this agent is the team leader
	logger     Logger                                                           // logger instance
	onReminder func(ctx context.Context, agentName string, reminderText string) // per-runner reminder callback
}

func newLifecycleManager(teamCfg *Config, runnerConf *RunnerConfig, isLeader bool, router *sourceRouter, pumpMgr *pumpManager) *lifecycleManager {
	return &lifecycleManager{
		registry:   newTeammateRegistry(),
		router:     router,
		pumpMgr:    pumpMgr,
		teamCfg:    teamCfg,
		runnerConf: runnerConf,
		isLeader:   isLeader,
		logger:     runnerConf.logger(),
	}
}

// SetPlantaskMW sets the plantask middleware. Called after construction because
// the plantask middleware requires the teamMiddleware (which holds this
// lifecycleManager) to already exist — a circular dependency at construction time.
func (lm *lifecycleManager) SetPlantaskMW(ptMW plantask.Middleware) {
	lm.ptMW = ptMW
}

// agentConfig returns the agent configuration from the runner config.
func (lm *lifecycleManager) agentConfig() *adk.ChatModelAgentConfig {
	return lm.runnerConf.AgentConfig
}

// buildTeammateAgent creates a teammate's ChatModelAgent with team and plantask middleware.
// The teammate's specific task prompt is delivered via the mailbox (sendInitialPrompt),
// not via the agent instruction — so no prompt parameter is needed here.
func (lm *lifecycleManager) buildTeammateAgent(ctx context.Context, agentName, teamName string) (*adk.ChatModelAgent, error) {
	tmMW := newTeamTeammateMiddleware(lm.runnerConf, agentName, teamName)

	extraInstruction := fmt.Sprintf(
		"Your agent name is: %s\n\n%s",
		agentName,
		selectToolDesc(teammateInstruction, teammateInstructionChinese),
	)

	tmAgent, ptMW, err := buildTeamAgent(ctx, lm.runnerConf, tmMW, extraInstruction, lm.onReminder)
	if err != nil {
		return nil, fmt.Errorf("create teammate agent: %w", err)
	}

	// Store plantask middleware reference so the teammate can operate on tasks.
	tmMW.lifecycle.SetPlantaskMW(ptMW)

	return tmAgent, nil
}

// plantaskMW returns the plantask middleware for task operations.
func (lm *lifecycleManager) plantaskMW() plantask.Middleware {
	return lm.ptMW
}

// hasMember checks whether the given member exists in the team configuration.
func (lm *lifecycleManager) hasMember(ctx context.Context, teamName, memberName string) (bool, error) {
	return lm.teamCfg.HasMember(ctx, teamName, memberName)
}

// mailbox creates a new mailbox instance for the given team and owner.
func (lm *lifecycleManager) mailbox(teamName, ownerName string) *mailbox {
	return newMailboxFromConfig(lm.teamCfg, teamName, ownerName)
}

func (lm *lifecycleManager) initInbox(ctx context.Context, teamName, ownerName string) error {
	return initInboxFile(ctx, lm.teamCfg.Backend, lm.inboxPath(teamName, ownerName))
}

func (lm *lifecycleManager) inboxPath(teamName, agentName string) string {
	return inboxFilePath(lm.teamCfg.BaseDir, teamName, agentName)
}

// startTeammateRunner registers the teammate and starts its runner goroutine.
// The goroutine automatically cleans up the teammate on exit via deferred
// cleanupExitedTeammate.
func (lm *lifecycleManager) startTeammateRunner(parentCtx context.Context,
	teamName, memberName string, result *teammateHandle, run func(context.Context) error) {

	lm.registry.register(memberName, result)

	lm.registry.addRunner()
	safeGoWithLogger(lm.logger, func() {
		defer lm.registry.doneRunner()
		// Use a timeout context for cleanup because parentCtx may already be
		// cancelled when the goroutine exits (e.g. ShutdownAllTeammates cancels the
		// context). Backend I/O in cleanup must not be short-circuited by cancellation,
		// but we cap the wait to prevent goroutine leaks if the backend hangs.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cleanupCancel()
		defer lm.cleanupExitedTeammate(cleanupCtx, teamName, memberName)
		err := run(parentCtx)
		if err != nil && !errors.Is(err, context.Canceled) {
			lm.logger.Printf("teammate runner finished with error: %v", err)
		}
	})
}

// cleanupFailedTeammateSpawn reverses a partially-completed teammate spawn:
// removes the member from config, deletes the inbox file, and unregisters
// the mailbox source and loop.
func (lm *lifecycleManager) cleanupFailedTeammateSpawn(ctx context.Context, teamName, memberName string) {
	if err := lm.teamCfg.RemoveMember(ctx, teamName, memberName); err != nil {
		lm.logger.Printf("cleanupFailedTeammateSpawn: remove member %q: %v", memberName, err)
	}
	if err := lm.teamCfg.Backend.Delete(ctx, &DeleteRequest{FilePath: lm.inboxPath(teamName, memberName)}); err != nil {
		lm.logger.Printf("cleanupFailedTeammateSpawn: delete inbox for %q: %v", memberName, err)
	}
	lm.pumpMgr.UnsetMailbox(memberName)
	lm.router.UnregisterLoop(memberName)
}

// stopTeammateRuntime cancels the teammate's context and unregisters
// mailbox/loop. Returns true if this call was the first to stop the teammate
// (i.e. the teammateHandle was still present in the registry), false if it was
// already stopped by a prior call (idempotent).
//
// NOTE: this intentionally does NOT call removeLock. The per-inbox lock must
// remain valid until the member is removed from config (RemoveMember) and the
// inbox file is deleted, so that concurrent senders who already passed the
// hasMember check still share the same lock. Callers are responsible for
// calling removeLock after RemoveMember + inbox deletion.
func (lm *lifecycleManager) stopTeammateRuntime(ctx context.Context, teamName, memberName string) bool {
	result, firstStop := lm.registry.remove(memberName)
	if firstStop {
		if result.Cancel != nil {
			result.Cancel()
		}
	}

	lm.pumpMgr.UnsetMailbox(memberName)
	lm.router.UnregisterLoop(memberName)
	return firstStop
}

// cleanupExitedTeammate is the deferred cleanup handler called when a teammate
// goroutine exits (gracefully or not). It stops the runtime, unassigns tasks,
// removes the member from config, and optionally notifies the leader.
func (lm *lifecycleManager) cleanupExitedTeammate(ctx context.Context, teamName, memberName string) {
	// Same order as removeTeammate: stop runtime first (idempotent if already
	// stopped), then unassign tasks, then remove from config.
	firstStop := lm.stopTeammateRuntime(ctx, teamName, memberName)
	unassigned, unassignErr := lm.unassignMemberTasks(ctx, memberName)
	if unassignErr != nil {
		lm.logger.Printf("cleanupExitedTeammate: unassign tasks for %q: %v", memberName, unassignErr)
	}
	if err := lm.teamCfg.RemoveMember(ctx, teamName, memberName); err != nil {
		lm.logger.Printf("cleanupExitedTeammate: remove member %q: %v", memberName, err)
	}
	// Delete inbox file to prevent a same-name teammate from inheriting stale messages.
	if err := lm.teamCfg.Backend.Delete(ctx, &DeleteRequest{FilePath: lm.inboxPath(teamName, memberName)}); err != nil {
		lm.logger.Printf("cleanupExitedTeammate: delete inbox for %q: %v", memberName, err)
	}
	// Release the per-inbox lock only after the member is removed from config
	// and the inbox file is deleted, so concurrent senders that already passed
	// hasMember still share the same lock instance.
	lm.teamCfg.removeLock(memberName)

	// Only send a terminated notification when this is the first cleanup for
	// the teammate (i.e. a non-graceful exit such as crash or context cancel).
	// When the teammate was already removed by the graceful shutdown-approval
	// path (removeTeammate → stopTeammateRuntime), firstStop is false and the
	// notification has already been sent via OnShutdownResponse — skip to avoid
	// duplicate notifications to the leader.
	if firstStop {
		lm.notifyLeaderTeammateTerminated(ctx, teamName, memberName, unassigned)
	}
}

// removeTeammate performs a graceful removal: stops the runtime, unassigns
// owned tasks, and removes the member from the team config.
func (lm *lifecycleManager) removeTeammate(ctx context.Context, teamName, memberName string) (unassigned []string, firstStop bool, err error) {
	// Stop the runtime first so a failed cleanup does not leave a live teammate
	// that is no longer reachable through the team config.
	firstStop = lm.stopTeammateRuntime(ctx, teamName, memberName)

	unassigned, unassignErr := lm.unassignMemberTasks(ctx, memberName)

	// Always attempt RemoveMember even if unassign failed, so the teammate
	// doesn't linger in config as a "dead" member that others try to message.
	if removeErr := lm.teamCfg.RemoveMember(ctx, teamName, memberName); removeErr != nil {
		if unassignErr != nil {
			return nil, firstStop, fmt.Errorf("unassign tasks for %q: %v; remove member: %w", memberName, unassignErr, removeErr)
		}
		return unassigned, firstStop, fmt.Errorf("remove member %q: %w", memberName, removeErr)
	}

	// Delete inbox file to prevent a same-name teammate from inheriting stale messages.
	if err := lm.teamCfg.Backend.Delete(ctx, &DeleteRequest{FilePath: lm.inboxPath(teamName, memberName)}); err != nil {
		lm.logger.Printf("removeTeammate: delete inbox for %q: %v", memberName, err)
	}
	// Release the per-inbox lock only after the member is removed from config
	// and the inbox file is deleted, so concurrent senders that already passed
	// hasMember still share the same lock instance.
	lm.teamCfg.removeLock(memberName)

	if unassignErr != nil {
		return nil, firstStop, fmt.Errorf("unassign tasks for %q: %w", memberName, unassignErr)
	}

	return unassigned, firstStop, nil
}

// unassignMemberTasks delegates to plantask Middleware which uses proper locking
// and the plantask task format. Returns nil if ptMW is not initialized (e.g. teammate
// cleanup during early shutdown).
func (lm *lifecycleManager) unassignMemberTasks(ctx context.Context, memberName string) ([]string, error) {
	if lm.ptMW == nil {
		return nil, nil
	}
	return lm.ptMW.UnassignOwnerTasks(ctx, memberName)
}

// buildTeammateTerminationMessage builds a human-readable termination notice
// including any tasks that were unassigned.
func buildTeammateTerminationMessage(name string, unassigned []string) string {
	msg := fmt.Sprintf("%s has shut down.", name)
	if len(unassigned) > 0 {
		msg += fmt.Sprintf(" %d task(s) were unassigned: #%s.", len(unassigned), strings.Join(unassigned, ", #"))
	}
	return msg
}

// notifyLeaderTeammateTerminated sends a teammate_terminated message to the
// leader's inbox so it learns about non-graceful teammate exits (crash,
// context cancel, etc.). Errors are best-effort and silently ignored because
// cleanup must not fail.
func (lm *lifecycleManager) notifyLeaderTeammateTerminated(ctx context.Context, teamName, memberName string, unassigned []string) {
	if !lm.isLeader {
		// Only the leader process owns the router and mailbox infra;
		// teammate processes must not try to push into it.
		return
	}
	notifyMsg := buildTeammateTerminationMessage(memberName, unassigned)
	sysMsg, err := buildTeammateTerminatedSystemMessage(notifyMsg)
	if err != nil {
		return
	}
	item := TurnInput{
		TargetAgent: LeaderAgentName,
		Messages:    []string{formatTeammateMessageEnvelope(sysMsg.From, sysMsg.Text, sysMsg.Summary)},
	}
	_, _ = lm.router.Push(item)
}

// setupMailbox initializes the inbox file, registers a MailboxMessageSource on the router,
// and starts the mailbox pump goroutine. This ensures no gap between inbox creation and
// pump startup where messages could be lost.
func (lm *lifecycleManager) setupMailbox(ctx context.Context, teamName, agentName string, sourceCfg *MailboxSourceConfig) error {
	if err := lm.initInbox(ctx, teamName, agentName); err != nil {
		return fmt.Errorf("create inbox file for %s: %w", agentName, err)
	}
	mb := lm.mailbox(teamName, agentName)
	ms := newMailboxMessageSource(mb, sourceCfg)
	lm.pumpMgr.SetMailbox(agentName, ms)
	lm.pumpMgr.StartPump(ctx, agentName)
	return nil
}

// startPump starts the mailbox pump goroutine for the given agent.
// Wraps pumpMgr.StartPump so tool layer doesn't access pumpMgr directly.
func (lm *lifecycleManager) startPump(ctx context.Context, agentName string) {
	if lm.pumpMgr != nil {
		lm.pumpMgr.StartPump(ctx, agentName)
	}
}

// createTeammateRunner creates a teammate's TurnLoop runner and registers it
// with the shared router and pump manager. This encapsulates the router/pumpMgr
// wiring so that tool implementations don't need to access them directly.
func (lm *lifecycleManager) createTeammateRunner(agent *adk.ChatModelAgent, agentName, teamName string) (*Runner, error) {
	return newTeammateRunner(lm.runnerConf, lm.router, lm.pumpMgr, agent, agentName, teamName)
}

// cleanupLeaderMailbox stops the leader's mailbox pump and releases its per-inbox
// lock. Called by TeamDelete to prevent goroutine leaks and memory accumulation.
func (lm *lifecycleManager) cleanupLeaderMailbox() {
	if lm.pumpMgr != nil {
		lm.pumpMgr.UnsetMailbox(LeaderAgentName)
	}
	lm.teamCfg.removeLock(LeaderAgentName)
}

// activeTeammateNames returns the names of teammates whose goroutines are still
// running (registered in the registry). This reflects actual runtime state, not
// the config-level IsActive flag which only tracks idle/busy status.
func (lm *lifecycleManager) activeTeammateNames() []string {
	return lm.registry.activeNames()
}

// shutdownAll cancels all active teammates and waits for their goroutines to exit.
func (lm *lifecycleManager) shutdownAll(logger Logger) {
	lm.registry.cancelAll()
	lm.registry.waitWithTimeout(logger, defaultShutdownTimeout)
}
