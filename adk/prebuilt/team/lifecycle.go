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
	"sync"

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
	store      *configStore                                                     // config.json read-modify-write operations
	runnerConf *RunnerConfig                                                    // full runner config, needed for teammate creation
	isLeader   bool                                                             // whether this agent is the team leader
	logger     Logger                                                           // logger instance
	onReminder func(ctx context.Context, agentName string, reminderText string) // per-runner reminder callback

	// rootCtxMu guards rootCtx. rootCtx is the long-lived team runtime context,
	// captured once when the Runner starts (Runner.Run → setRootContext). Teammate
	// runner goroutines are derived from this context — NOT from the per-turn tool
	// call context — so a background teammate outlives the single assistant turn
	// that spawned it. The tool call's own ctx can be a short-lived per-turn ctx
	// (e.g. when the host supplies GenInputResult.RunCtx with a per-turn deadline);
	// binding teammates to it would cancel them as soon as that turn ends, which
	// contradicts the "background teammate survives across turns" contract.
	// Teammate lifetime is instead governed by explicit teardown (TeamDelete /
	// shutdown_request / Runner shutdown), which cancels each teammate's derived
	// context via its registered Cancel func.
	rootCtxMu sync.RWMutex
	rootCtx   context.Context
}

func newLifecycleManager(teamCfg *Config, runnerConf *RunnerConfig, isLeader bool, router *sourceRouter, pumpMgr *pumpManager) *lifecycleManager {
	return &lifecycleManager{
		registry:   newTeammateRegistry(),
		router:     router,
		pumpMgr:    pumpMgr,
		teamCfg:    teamCfg,
		store:      newConfigStore(teamCfg),
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

// setRootContext records the long-lived team runtime context. It is called once
// when the Runner starts (Runner.Run). Teammate runner goroutines are derived
// from this context rather than from the per-turn tool call context, so a
// background teammate is not cancelled when the assistant turn that spawned it
// ends. See the rootCtx field doc for the full rationale.
func (lm *lifecycleManager) setRootContext(ctx context.Context) {
	lm.rootCtxMu.Lock()
	lm.rootCtx = ctx
	lm.rootCtxMu.Unlock()
}

// teammateRootContext returns the context teammate runners should be derived
// from. It prefers the team runtime root context captured by setRootContext;
// before the Runner has started (root not yet set, e.g. in unit tests that spawn
// directly) it falls back to the supplied tool ctx so behaviour degrades to the
// previous per-call binding rather than panicking on a nil context.
func (lm *lifecycleManager) teammateRootContext(toolCtx context.Context) context.Context {
	lm.rootCtxMu.RLock()
	root := lm.rootCtx
	lm.rootCtxMu.RUnlock()
	if root != nil {
		return root
	}
	return toolCtx
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
	return lm.store.HasMember(ctx, teamName, memberName)
}

// createTeam creates a new team (directory layout + config.json) and returns the
// resolved team config. Exposed so the TeamCreate tool does not reach into the
// config store directly (infrastructure access is centralized in lifecycle; see
// the package doc comment in types.go).
func (lm *lifecycleManager) createTeam(ctx context.Context, teamName, description, leaderName, leaderType string) (*teamConfig, error) {
	return lm.store.CreateTeam(ctx, teamName, description, leaderName, leaderType)
}

// deleteTeam removes the persisted team (config.json, inbox, and task
// directories) for the given team name.
func (lm *lifecycleManager) deleteTeam(ctx context.Context, teamName string) error {
	return lm.store.DeleteTeam(ctx, teamName)
}

// nonLeaderMemberNames returns the names of all non-leader members still listed
// in config.json. TeamDelete uses it to detect residual members left by an
// incomplete cleanup or a process restart.
func (lm *lifecycleManager) nonLeaderMemberNames(ctx context.Context, teamName string) ([]string, error) {
	return lm.store.NonLeaderMemberNames(ctx, teamName)
}

// addTeammateMember registers a teammate in config.json with a deduplicated name
// and returns the stored member (whose Name may differ from the requested one if
// a collision was resolved).
func (lm *lifecycleManager) addTeammateMember(ctx context.Context, teamName string, member teamMember) (teamMember, error) {
	return lm.store.AddMemberWithDeduplicatedName(ctx, teamName, member)
}

// configFilePath returns the on-disk path of the team's config.json.
func (lm *lifecycleManager) configFilePath(teamName string) string {
	return lm.store.configFilePath(teamName)
}

// leadAgentID returns the leader's agent ID for the given team.
func (lm *lifecycleManager) leadAgentID(teamName string) string {
	return lm.store.LeadAgentID(teamName)
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

// teardownOptions controls how teardownTeammate performs a teammate removal.
type teardownOptions struct {
	// stopRuntime cancels the teammate goroutine and unregisters its mailbox/loop
	// before touching config. Spawn-failure cleanup leaves this false because the
	// runner was never registered.
	stopRuntime bool
	// unassignTasks reassigns the member's owned tasks back to the pool. Spawn
	// failure happens before any task could be owned, so it leaves this false.
	unassignTasks bool
}

// teardownResult reports what teardownTeammate did so callers can decide on
// follow-up actions (e.g. leader notification).
type teardownResult struct {
	firstStop  bool
	unassigned []string
}

// teardownTeammate is the single, idempotent removal path shared by the
// graceful (removeTeammate), goroutine-exit (cleanupExitedTeammate), and
// spawn-failure (cleanupFailedTeammateSpawn) flows. It performs, in order:
// stop runtime (optional) → unassign tasks (optional) → delete inbox file →
// RemoveMember. Each step is best-effort: errors are returned joined so the
// caller can log them, but later steps always run so a teammate can never linger
// half-removed in config.
//
// Ordering note (inbox delete BEFORE RemoveMember): the goroutine-exit cleanup
// path does NOT hold teamOpLock, so it can interleave with a concurrent Agent
// spawn that reuses the same member name. If RemoveMember ran first, the freed
// name could be re-registered and its inbox re-created (with the new teammate's
// initial prompt) by the spawn before this path deleted the inbox — clobbering
// the new inbox and silently dropping that prompt. Deleting the inbox while the
// name is still reserved in config closes that window: a concurrent spawn either
// observes the name as taken and deduplicates to a different inbox, or its
// AddMember (and thus initInbox) is serialized by cfgLock to run strictly after
// this RemoveMember, so the inbox it creates is never the one deleted here.
func (lm *lifecycleManager) teardownTeammate(ctx context.Context, teamName, memberName string, opts teardownOptions) (teardownResult, error) {
	var res teardownResult
	var errs []error

	if opts.stopRuntime {
		res.firstStop = lm.stopTeammateRuntime(ctx, teamName, memberName)
	} else {
		// No runtime to stop, but still detach any messaging registrations so a
		// failed spawn does not leave a dangling mailbox/loop behind.
		lm.pumpMgr.UnsetMailbox(memberName)
		lm.router.UnregisterLoop(memberName)
	}

	if opts.unassignTasks {
		unassigned, unassignErr := lm.unassignMemberTasks(ctx, memberName)
		if unassignErr != nil {
			errs = append(errs, fmt.Errorf("unassign tasks for %q: %w", memberName, unassignErr))
		}
		res.unassigned = unassigned
	}

	// Delete inbox file before RemoveMember so a same-name teammate cannot be
	// registered and have its fresh inbox clobbered by this delete (see the
	// ordering note above). Deleting it also prevents a future same-name teammate
	// from inheriting stale messages. The delete goes through mailbox.DeleteInbox
	// so it holds the same per-inbox write lock as sendToOne/MarkRead: this
	// serializes the removal against concurrent senders (a member can still pass
	// membership validation until RemoveMember below), so a send cannot interleave
	// with the delete to resurrect the inbox or lose its write. The per-inbox lock
	// is reference counted (ForName/Release) and reclaimed automatically once no
	// holder remains, so there is nothing to remove explicitly here.
	if delErr := lm.mailbox(teamName, LeaderAgentName).DeleteInbox(ctx, memberName); delErr != nil {
		errs = append(errs, fmt.Errorf("delete inbox for %q: %w", memberName, delErr))
	}

	if removeErr := lm.store.RemoveMember(ctx, teamName, memberName); removeErr != nil {
		errs = append(errs, fmt.Errorf("remove member %q: %w", memberName, removeErr))
	}

	return res, joinErrors(errs...)
}

// cleanupFailedTeammateSpawn reverses a partially-completed teammate spawn:
// removes the member from config, deletes the inbox file, and unregisters
// the mailbox source and loop.
func (lm *lifecycleManager) cleanupFailedTeammateSpawn(ctx context.Context, teamName, memberName string) {
	if _, err := lm.teardownTeammate(ctx, teamName, memberName, teardownOptions{}); err != nil {
		lm.logger.Printf("cleanupFailedTeammateSpawn: %v", err)
	}
}

// stopTeammateRuntime cancels the teammate's context and unregisters
// mailbox/loop. Returns true if this call was the first to stop the teammate
// (i.e. the teammateHandle was still present in the registry), false if it was
// already stopped by a prior call (idempotent).
//
// NOTE: per-inbox locks are reference counted inside the mailbox operations
// (ForName/Release), so there is no separate lock-removal step to coordinate
// here. The lock is reclaimed automatically once no send/read holds a reference.
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
	res, err := lm.teardownTeammate(ctx, teamName, memberName, teardownOptions{
		stopRuntime:   true,
		unassignTasks: true,
	})
	if err != nil {
		lm.logger.Printf("cleanupExitedTeammate: %v", err)
	}

	// Only send a terminated notification when this is the first cleanup for
	// the teammate (i.e. a non-graceful exit such as crash or context cancel).
	// When the teammate was already removed by the graceful shutdown-approval
	// path (removeTeammate → stopTeammateRuntime), firstStop is false and the
	// notification has already been sent via OnShutdownResponse — skip to avoid
	// duplicate notifications to the leader.
	//
	// Pass the teardown error through so a partial cleanup failure is reported to
	// the leader, not just logged: this is the only notification the leader gets
	// for a crashed teammate, so swallowing the error here would hide residual
	// state (config member, inbox, or task) with no other signal.
	if res.firstStop {
		lm.notifyLeaderTeammateTerminated(ctx, teamName, memberName, res.unassigned, err)
	}
}

// removeTeammate performs a graceful removal: stops the runtime, unassigns
// owned tasks, and removes the member from the team config.
func (lm *lifecycleManager) removeTeammate(ctx context.Context, teamName, memberName string) (unassigned []string, firstStop bool, err error) {
	res, teardownErr := lm.teardownTeammate(ctx, teamName, memberName, teardownOptions{
		stopRuntime:   true,
		unassignTasks: true,
	})
	return res.unassigned, res.firstStop, teardownErr
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
// including any tasks that were unassigned and, when cleanupErr is non-nil, a
// warning that teardown only partially succeeded.
//
// Surfacing cleanupErr in the leader-facing message (not just the log) is
// deliberate: teammate teardown is best-effort and never retried from the
// mailbox, so a swallowed inbox-delete / RemoveMember / unassign failure would
// otherwise leave residual config members, undeleted inboxes, or stuck tasks
// that the leader has no way to learn about. The warning tells the leader the
// teammate is gone but the team state may need a TeamDelete(force=true) or a
// manual retry to fully reconcile.
func buildTeammateTerminationMessage(name string, unassigned []string, cleanupErr error) string {
	msg := fmt.Sprintf("%s has shut down.", name)
	if len(unassigned) > 0 {
		msg += fmt.Sprintf(" %d task(s) were unassigned: #%s.", len(unassigned), strings.Join(unassigned, ", #"))
	}
	if cleanupErr != nil {
		msg += fmt.Sprintf(" WARNING: cleanup only partially completed (%v); the team may retain residual"+
			" config member(s), undeleted inbox(es), or unreassigned task(s). Verify with TeamDelete (force=true if needed).", cleanupErr)
	}
	return msg
}

// notifyLeaderTeammateTerminated sends a teammate_terminated message to the
// leader's inbox so it learns about non-graceful teammate exits (crash,
// context cancel, etc.). Failures are best-effort because cleanup must not
// fail, but they are logged so a dropped notification is observable.
func (lm *lifecycleManager) notifyLeaderTeammateTerminated(ctx context.Context, teamName, memberName string, unassigned []string, cleanupErr error) {
	if !lm.isLeader {
		// Only the leader process owns the router and mailbox infra;
		// teammate processes must not try to push into it.
		return
	}
	notifyMsg := buildTeammateTerminationMessage(memberName, unassigned, cleanupErr)
	sysMsg, err := buildTeammateTerminatedSystemMessage(notifyMsg)
	if err != nil {
		// A marshal failure here is a programming error (the payload is built from
		// internal types), not a transient I/O fault, so always surface it rather
		// than dropping the leader notification silently.
		lm.logger.Printf("notifyLeaderTeammateTerminated: build system message for %q: %v", memberName, err)
		return
	}
	item := TurnInput{
		TargetAgent: LeaderAgentName,
		Messages:    []string{formatTeammateMessageEnvelope(sysMsg.From, renderProtocolText(sysMsg.Text), sysMsg.Summary)},
	}
	// A dropped push (leader loop already unregistered during teardown) is not
	// fatal, but log it so the lost termination notice is not invisible.
	if accepted, _ := lm.router.Push(item); !accepted {
		lm.logger.Printf("notifyLeaderTeammateTerminated: leader loop unavailable, dropped termination notice for %q", memberName)
	}
}

// setupMailbox initializes the inbox file, registers a mailboxMessageSource on the router,
// and starts the mailbox pump goroutine. This ensures no gap between inbox creation and
// pump startup where messages could be lost.
func (lm *lifecycleManager) setupMailbox(ctx context.Context, teamName, agentName string, sourceCfg *mailboxSourceConfig) error {
	if err := lm.initInbox(ctx, teamName, agentName); err != nil {
		return fmt.Errorf("create inbox file for %s: %w", agentName, err)
	}
	mb := lm.mailbox(teamName, agentName)
	ms := newMailboxMessageSource(mb, sourceCfg)
	lm.pumpMgr.SetMailbox(agentName, ms)
	lm.startPump(ctx, agentName)
	return nil
}

// startPump starts the mailbox pump goroutine for the given agent.
// Wraps pumpMgr.StartPump so tool layer doesn't access pumpMgr directly.
// pumpMgr may be nil for teammate managers; StartPump is nil-safe.
func (lm *lifecycleManager) startPump(ctx context.Context, agentName string) {
	lm.pumpMgr.StartPump(ctx, agentName)
}

// createTeammateRunner creates a teammate's TurnLoop runner and registers it
// with the shared router and pump manager. This encapsulates the router/pumpMgr
// wiring so that tool implementations don't need to access them directly.
func (lm *lifecycleManager) createTeammateRunner(agent *adk.ChatModelAgent, agentName, teamName string) (*Runner, error) {
	return newTeammateRunner(lm.runnerConf, lm.router, lm.pumpMgr, agent, agentName, teamName)
}

// cleanupLeaderMailbox stops the leader's mailbox pump. Called by TeamDelete to
// prevent goroutine leaks. The leader's per-inbox lock is reference counted in
// the mailbox operations (ForName/Release) and reclaimed automatically once no
// send/read holds it, so no explicit lock removal is needed here.
// pumpMgr may be nil for teammate managers; UnsetMailbox is nil-safe.
func (lm *lifecycleManager) cleanupLeaderMailbox() {
	lm.pumpMgr.UnsetMailbox(LeaderAgentName)
}

// activeTeammateNames returns the names of teammates whose goroutines are still
// running (registered in the registry). This reflects actual runtime state;
// busy/idle status is tracked in process and never persisted to config.json.
func (lm *lifecycleManager) activeTeammateNames() []string {
	return lm.registry.activeNames()
}

// shutdownAll cancels all active teammates and waits for their goroutines to
// exit. The wait honors ctx (so a caller can bound teardown to an external
// deadline) and is additionally capped at defaultShutdownTimeout so a hung
// backend cannot block shutdown indefinitely.
func (lm *lifecycleManager) shutdownAll(ctx context.Context, logger Logger) {
	lm.registry.cancelAll()
	lm.registry.waitWithTimeout(ctx, logger, defaultShutdownTimeout)
}
