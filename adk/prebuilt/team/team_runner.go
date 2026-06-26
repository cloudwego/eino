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

// team_runner.go provides Runner, the top-level orchestrator that wires
// together TurnLoop, teamMiddleware, sourceRouter, and plantask for
// multi-agent team execution.

package team

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

// RunnerConfig configures a Runner.
//
// Each RunnerConfig (including its TeamConfig) should be used for a single
// Runner / request. Reusing the same *Config across multiple concurrent
// Runners is safe but discouraged: the internal locks inside Config are
// per-Config rather than per-team, so concurrent Runners would serialize
// unnecessarily on unrelated teams.
type RunnerConfig struct {
	// AgentConfig is the configuration for the agent. Required.
	// NewRunner automatically prepends the team leader middleware to Handlers.
	AgentConfig *adk.ChatModelAgentConfig

	// TeamConfig contains team-specific settings (Backend, BaseDir, Model). Required.
	TeamConfig *Config

	// GenInput receives the TurnLoop instance and all buffered items, and decides
	// what to process. It returns which items to consume now vs keep for later turns.
	// Required.
	GenInput func(ctx context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error)

	// OnAgentEvents is called to handle events emitted by the agent.
	// The TurnContext provides per-turn info and control.
	// Required: NewRunner returns an error if this is nil. The handler is
	// responsible for draining the agent event stream; failing to consume events
	// can block or stall the TurnLoop, so a no-op drain must be supplied if the
	// caller does not need the events.
	OnAgentEvents func(ctx context.Context, tc *adk.TurnContext[TurnInput, adk.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error

	// TeammateRoles declares reusable teammate roles the leader can spawn by passing
	// their Name as the Agent tool's subagent_type. Optional.
	//
	// The available roles (name + description + tool summary) are rendered into the
	// leader's instruction so it knows which subagent_type values exist and when to
	// use each, and spawning a teammate with a role overlays that role's Model /
	// Tools / Instruction onto AgentConfig. subagent_type is required and must match
	// a declared role; an empty or unmatched value is rejected by the Agent tool and
	// surfaced back to the model to retry.
	//
	// When this list is empty, the framework injects a single default
	// "general-purpose" role (inheriting the leader's model and tools) so there is
	// always exactly one valid subagent_type. Supplying roles replaces that default
	// with the given set, which is then treated as the exhaustive allowlist.
	TeammateRoles []TeammateRole

	// Logger is the logger used by the team middleware.
	// If nil, the standard log package is used.
	Logger Logger
}

// logger returns the configured Logger, falling back to the standard log package.
func (c *RunnerConfig) logger() Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return defaultLogger{}
}

// Runner wraps the TurnLoop lifecycle with multi-agent routing
// and per-agent conversation history management.
//
// The team is created when NewRunner returns and removed when Wait/WaitContext
// returns (unless TeamConfig.RetainDataOnExit is set), so the agent never has to
// create or delete a team itself.
type Runner struct {
	loop     *adk.TurnLoop[TurnInput, adk.Message]
	leaderMW *teamMiddleware
	router   *sourceRouter

	teamName         string
	retainDataOnExit bool
}

// NewRunner creates a new Runner with multi-agent routing support.
// It creates the team leader middleware, prepends it to AgentConfig.Handlers,
// constructs the ChatModelAgent, and wires up the TurnLoop.
//
// NewRunner also creates the team itself: it writes the team directory layout
// and config.json, then registers the leader's inbox so teammates can message it
// the moment they are spawned. The team name comes from TeamConfig.Name, or is
// generated when that is empty. On any failure after the team directory is
// created, NewRunner rolls it back so a failed construction leaves no residue.
func NewRunner(ctx context.Context, conf *RunnerConfig) (*Runner, error) {
	if conf == nil {
		return nil, fmt.Errorf("RunnerConfig is required")
	}
	if conf.AgentConfig == nil {
		return nil, fmt.Errorf("AgentConfig is required")
	}
	if err := conf.TeamConfig.validate(); err != nil {
		return nil, err
	}
	if conf.GenInput == nil {
		return nil, fmt.Errorf("GenInput is required")
	}
	if conf.OnAgentEvents == nil {
		return nil, fmt.Errorf("OnAgentEvents is required")
	}

	conf.TeamConfig.ensureInit()

	registry, err := newSubagentRegistry(conf.TeammateRoles)
	if err != nil {
		return nil, fmt.Errorf("invalid TeammateRoles: %w", err)
	}

	router := newSourceRouter(LeaderAgentName, conf.logger())
	pumpMgr := newPumpManager(router, conf.logger())

	// onReminder is bound to this runner's router — not stored on the shared
	// Config — so parallel runners over the same *Config each get their own
	// callback and never overwrite each other.
	onReminder := func(_ context.Context, agentName string, reminderText string) {
		// A dropped push (the target loop is being torn down) is not fatal, but
		// log it so a lost reminder is observable — mirroring the same
		// accepted-check convention used by notifyLeaderTeammateTerminated.
		if accepted, _ := router.Push(TurnInput{
			TargetAgent: agentName,
			Messages:    []string{reminderText},
		}); !accepted {
			conf.logger().Printf("onReminder: loop for %q unavailable, dropped reminder", agentName)
		}
	}

	leaderMW := newTeamLeadMiddleware(conf, router, pumpMgr)
	leaderMW.lifecycle.onReminder = onReminder
	leaderMW.lifecycle.subagents = registry

	// Create the team up front: directory layout, config.json with the leader as
	// the first member, and the leader's registered inbox. This replaces the old
	// TeamCreate tool — the agent no longer creates a team itself.
	teamName, err := setupTeam(ctx, conf, leaderMW)
	if err != nil {
		return nil, err
	}
	leaderMW.setTeamName(teamName)

	rollback := func() {
		// The tool call's ctx may already be cancelled on the error path; use a
		// fresh bounded context so cleanup still runs. Mirrors cleanupExitedTeammate.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		leaderMW.lifecycle.cleanupLeaderMailbox()
		if delErr := leaderMW.lifecycle.deleteTeam(cleanupCtx, teamName); delErr != nil {
			conf.logger().Printf("NewRunner rollback: delete team %q: %v", teamName, delErr)
		}
	}

	extraInstruction := selectToolDesc(leaderInstruction, leaderInstructionChinese)
	// Append the available subagent types so the leader knows which subagent_type
	// values exist and when to use each (no-op when no roles are configured).
	if types := renderAvailableSubagentTypes(registry); types != "" {
		extraInstruction += "\n\n" + types
	}
	agent, ptMW, err := buildTeamAgent(ctx, conf, leaderMW, conf.AgentConfig, extraInstruction, onReminder)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("create leader agent: %w", err)
	}
	leaderMW.lifecycle.SetPlantaskMW(ptMW)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: conf.GenInput,
		PrepareAgent: func(_ context.Context, _ *adk.TurnLoop[TurnInput, adk.Message], _ []TurnInput) (adk.Agent, error) {
			return agent, nil
		},
		OnAgentEvents: conf.OnAgentEvents,
	})

	router.RegisterLoop(LeaderAgentName, loop)

	return &Runner{
		loop:             loop,
		leaderMW:         leaderMW,
		router:           router,
		teamName:         teamName,
		retainDataOnExit: conf.TeamConfig.RetainDataOnExit,
	}, nil
}

// setupTeam resolves the team name (generating one when unset), creates the team
// directory layout and config.json, and registers the leader's inbox (without
// starting its pump — that happens in Run, bound to the long-lived team runtime
// context). It returns the resolved team name. On a mailbox-registration failure
// it rolls back the just-created team directory so no residue is left behind.
func setupTeam(ctx context.Context, conf *RunnerConfig, leaderMW *teamMiddleware) (string, error) {
	name := conf.TeamConfig.Name
	if name == "" {
		name = generateTeamName()
	}
	if err := validateTeamName(name); err != nil {
		return "", fmt.Errorf("invalid team name: %w", err)
	}

	team, err := leaderMW.lifecycle.createTeam(ctx, name, "", LeaderAgentName, conf.AgentConfig.Name)
	if err != nil {
		return "", fmt.Errorf("create team: %w", err)
	}
	// createTeam may append a suffix on collision; use the resolved name.
	resolved := team.Name

	if err := leaderMW.lifecycle.registerMailbox(ctx, resolved, LeaderAgentName, &mailboxSourceConfig{
		OwnerName:          LeaderAgentName,
		Role:               teamRoleLeader,
		OnShutdownResponse: leaderMW.lifecycle.makeLeaderShutdownResponseHandler(resolved),
		Logger:             conf.logger(),
	}); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
		defer cancel()
		if delErr := leaderMW.lifecycle.deleteTeam(cleanupCtx, resolved); delErr != nil {
			conf.logger().Printf("setupTeam rollback: delete team %q: %v", resolved, delErr)
		}
		return "", fmt.Errorf("register leader mailbox: %w", err)
	}

	return resolved, nil
}

// generateTeamName returns a unique default team name for a Runner whose
// TeamConfig.Name was left empty. Backed by a UUID so concurrent Runners over the
// same BaseDir never collide.
func generateTeamName() string {
	return "team-" + uuid.New().String()
}

// Push pushes a TurnInput into the Runner's TurnLoop buffer.
// Items are routed to the appropriate agent's loop by the source router.
// Returns (accepted, ack) where ack is non-nil only for preemptive pushes.
func (r *Runner) Push(item TurnInput, opts ...adk.PushOption[TurnInput, adk.Message]) (bool, <-chan struct{}) {
	return r.router.Push(item, opts...)
}

// Run starts the TurnLoop. It is non-blocking: the loop runs in the background.
// Use Wait to block until the loop exits.
//
// The ctx passed here is captured as the team runtime root context: background
// teammates spawned by the Agent tool derive their runtime context from it
// rather than from the per-turn tool call context, so a teammate survives across
// assistant turns and is only torn down by explicit shutdown or when this ctx is
// cancelled. The leader's mailbox pump is also started here (bound to this ctx),
// so leader-directed messages flow as soon as the loop runs.
func (r *Runner) Run(ctx context.Context) {
	if r.leaderMW != nil {
		r.leaderMW.lifecycle.setRootContext(ctx)
		// Start the leader's mailbox pump now that the long-lived team runtime
		// context is available. The inbox and mailbox source were already
		// registered by NewRunner (registerMailbox), so this only attaches the
		// pump goroutine. Binding it to ctx (not the construction ctx) ties the
		// pump's lifetime to the run, and the TurnLoop is already registered so
		// StartPump finds it.
		r.leaderMW.lifecycle.startPump(ctx, LeaderAgentName)
	}
	r.loop.Run(ctx)
}

// Wait blocks until the TurnLoop exits and all teammate shutdown/cleanup
// has completed, then returns the exit state. Teammate teardown is bounded by
// an internal default timeout; use WaitContext to additionally bound it by an
// external deadline.
func (r *Runner) Wait() *adk.TurnLoopExitState[TurnInput, adk.Message] {
	return r.WaitContext(context.Background())
}

// WaitContext is like Wait but lets the caller bound teammate shutdown/cleanup
// with ctx. The TurnLoop itself is always awaited to completion; ctx only
// governs how long the post-loop teammate teardown waits before giving up
// (teardown is still capped internally by defaultShutdownTimeout). This lets a
// host (e.g. a server's graceful-stop path) cap how long exit can take.
//
// After teammates are torn down and the leader pump is stopped, the team's
// on-disk data is removed unless TeamConfig.RetainDataOnExit was set. This
// replaces the old TeamDelete tool — cleanup is now tied to the Runner's life.
func (r *Runner) WaitContext(ctx context.Context) *adk.TurnLoopExitState[TurnInput, adk.Message] {
	state := r.loop.Wait()
	if r.leaderMW != nil {
		if r.teamName != "" {
			r.leaderMW.ShutdownAllTeammates(ctx)
		}
		// Stop the leader's own mailbox pump to prevent a goroutine leak. It is
		// not covered by ShutdownAllTeammates (which only handles teammate pumps).
		r.leaderMW.lifecycle.cleanupLeaderMailbox()

		// Remove the team's on-disk data unless the caller asked to retain it.
		// Use a fresh bounded context so cleanup runs even if ctx is already
		// cancelled (mirrors the teammate teardown cleanup contexts).
		if !r.retainDataOnExit && r.teamName != "" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
			defer cancel()
			if err := r.leaderMW.lifecycle.deleteTeam(cleanupCtx, r.teamName); err != nil {
				r.leaderMW.logger().Printf("WaitContext: delete team %q: %v", r.teamName, err)
			}
		}
	}
	return state
}

// Stop signals the loop to stop and returns immediately.
func (r *Runner) Stop(opts ...adk.StopOption) {
	r.loop.Stop(opts...)
}

// newTeammateRunner creates a minimal Runner for a teammate.
func newTeammateRunner(conf *RunnerConfig, router *sourceRouter, pumpMgr *pumpManager,
	agent *adk.ChatModelAgent, agentName, teamName string) (*Runner, error) {

	tmMailbox := newMailboxFromConfig(conf.TeamConfig, teamName, agentName)

	mailboxSource := newMailboxMessageSource(tmMailbox, &mailboxSourceConfig{
		OwnerName: agentName,
		Role:      teamRoleTeammate,
		Logger:    conf.logger(),
	})

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: conf.GenInput,
		PrepareAgent: func(_ context.Context, _ *adk.TurnLoop[TurnInput, adk.Message], _ []TurnInput) (adk.Agent, error) {
			return agent, nil
		},
		OnAgentEvents: conf.OnAgentEvents,
	})

	router.RegisterLoop(agentName, loop)
	pumpMgr.SetMailbox(agentName, mailboxSource)

	return &Runner{
		loop:   loop,
		router: router,
	}, nil
}

// buildTeamAgent creates a ChatModelAgent with properly wired team and plantask
// middleware. It prepends teamMW + plantask to the handler chain (stripping any
// user-provided plantask middleware), applies extraInstruction if non-empty, and
// returns the agent along with the typed plantask.Middleware for task operations.
//
// baseConfig is the ChatModelAgentConfig to build from: NewRunner passes
// conf.AgentConfig for the leader, while agentTool.buildTeammateAgent passes a
// per-teammate config (the leader's config, optionally overlaid with a
// TeammateRole's Model / Tools / Instruction). baseConfig is copied by value
// before mutation so the caller's config is never modified.
//
// This is the single factory used by both NewRunner (leader) and
// agentTool.buildTeammateAgent (teammate) to avoid duplicating the
// middleware-wiring logic.
func buildTeamAgent(ctx context.Context, conf *RunnerConfig, teamMW *teamMiddleware, baseConfig *adk.ChatModelAgentConfig, extraInstruction string, onReminder func(ctx context.Context, agentName string, reminderText string)) (*adk.ChatModelAgent, plantask.Middleware, error) {
	defaultHandlers := []adk.ChatModelAgentMiddleware{teamMW}

	ptMWRaw, err := newTeamPlantaskMiddleware(ctx, conf.TeamConfig, teamMW, onReminder)
	if err != nil {
		return nil, nil, fmt.Errorf("create plantask middleware: %w", err)
	}
	defaultHandlers = append(defaultHandlers, ptMWRaw)

	ptMW, ok := ptMWRaw.(plantask.Middleware)
	if !ok {
		return nil, nil, fmt.Errorf("plantask middleware does not implement plantask.Middleware")
	}

	handlers := append(defaultHandlers, stripPlantaskMiddleware(baseConfig.Handlers)...)

	newConfig := *baseConfig
	newConfig.Handlers = handlers
	if extraInstruction != "" {
		newConfig.Instruction = fmt.Sprintf("%s\n%s", newConfig.Instruction, extraInstruction)
	}

	agent, err := adk.NewChatModelAgent(ctx, &newConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("create agent: %w", err)
	}

	return agent, ptMW, nil
}

// resolveReminderInterval maps a Config.Interval value to the interval passed to
// plantask.WithReminder. The zero value means "unset" and falls back to
// plantask's own default; only an explicitly negative value disables reminders.
// Reusing plantask.DefaultReminderInterval (rather than a local copy) keeps the
// team default in lockstep with plantask's, so the "Interval left unset" path
// never silently overwrites plantask's default with a stale duplicate value or
// with 0 (which would turn reminders off).
func resolveReminderInterval(interval int) int {
	if interval == 0 {
		return plantask.DefaultReminderInterval
	}
	return interval
}

// newTeamPlantaskMiddleware creates a plantask middleware configured for team mode.
// It wires up the task directory resolver, agent name resolver, and task assignment notifier.
func newTeamPlantaskMiddleware(ctx context.Context, teamCfg *Config, mw *teamMiddleware, onReminder func(ctx context.Context, agentName string, reminderText string)) (adk.ChatModelAgentMiddleware, error) {
	reminderInterval := resolveReminderInterval(teamCfg.Interval)

	store := newConfigStore(teamCfg)

	return plantask.New(ctx, &plantask.Config{
		Backend: teamCfg.Backend,
		BaseDir: teamCfg.BaseDir,
	},
		plantask.WithSharedTaskLock(teamCfg.state.taskLock),
		plantask.WithTaskAssignedHook(
			newTaskAssignedNotifier(teamCfg, func() string {
				return mw.getTeamName()
			}),
		),
		plantask.WithOwnerValidator(func(ctx context.Context, owner string) error {
			// Reject task assignments to identities that are not real members of
			// the active team, so a TaskUpdate cannot create an orphaned task whose
			// owner has no inbox / TurnLoop to consume the assignment notification.
			teamName := mw.getTeamName()
			if teamName == "" {
				return nil
			}
			exists, err := store.HasMember(ctx, teamName, owner)
			if err != nil {
				return fmt.Errorf("check owner %q membership: %w", owner, err)
			}
			if !exists {
				return fmt.Errorf("owner %q is not a member of team %q", owner, teamName)
			}
			return nil
		}),
		plantask.WithTaskBaseDirResolver(func(_ context.Context) string {
			return tasksDirPath(teamCfg.BaseDir, mw.getTeamName())
		}),
		plantask.WithTaskGuard(func(_ context.Context) error {
			// The team is created before the Runner returns, so getTeamName() is
			// normally non-empty by the time any task tool runs. This guard is a
			// defensive backstop: if the name is somehow empty, the resolved task
			// directory would collapse to {BaseDir}/tasks instead of the
			// team-scoped {BaseDir}/tasks/{teamName}, orphaning tasks. Reject task
			// operations in that window so tasks are never written outside a team.
			if mw.getTeamName() == "" {
				return fmt.Errorf("no active team; task operations are unavailable")
			}
			return nil
		}),
		plantask.WithAgentNameResolver(func(_ context.Context) string {
			return mw.agentName
		}),
		plantask.WithReminder(reminderInterval, func(ctx context.Context, reminderText string) {
			if onReminder == nil {
				return
			}
			onReminder(ctx, mw.agentName, reminderText)
		}),
		// Route plantask's best-effort diagnostics through the runner's logger so
		// they share the host's structured logging instead of bypassing it via the
		// standard log package.
		plantask.WithLogger(mw.logger()),
	)
}

// stripPlantaskMiddleware removes any user-provided plantask middleware from handlers.
// The team layer always injects its own team-aware plantask middleware with the
// correct resolvers and hooks, so user-provided instances must be replaced.
func stripPlantaskMiddleware(handlers []adk.ChatModelAgentMiddleware) []adk.ChatModelAgentMiddleware {
	result := make([]adk.ChatModelAgentMiddleware, 0, len(handlers))
	for _, h := range handlers {
		if _, ok := h.(plantask.Middleware); !ok {
			result = append(result, h)
		}
	}
	return result
}
