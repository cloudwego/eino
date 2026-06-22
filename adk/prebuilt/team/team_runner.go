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
type Runner struct {
	loop     *adk.TurnLoop[TurnInput, adk.Message]
	leaderMW *teamMiddleware
	router   *sourceRouter
}

// NewRunner creates a new Runner with multi-agent routing support.
// It creates the team leader middleware, prepends it to AgentConfig.Handlers,
// constructs the ChatModelAgent, and wires up the TurnLoop.
func NewRunner(ctx context.Context, conf *RunnerConfig) (*Runner, error) {
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

	router := newSourceRouter(LeaderAgentName, conf.logger())
	pumpMgr := newPumpManager(router, conf.logger())

	// onReminder is bound to this runner's router — not stored on the shared
	// Config — so parallel runners over the same *Config each get their own
	// callback and never overwrite each other.
	onReminder := func(_ context.Context, agentName string, reminderText string) {
		router.Push(TurnInput{
			TargetAgent: agentName,
			Messages:    []string{reminderText},
		})
	}

	leaderMW := newTeamLeadMiddleware(conf, router, pumpMgr)
	leaderMW.lifecycle.onReminder = onReminder

	agent, ptMW, err := buildTeamAgent(ctx, conf, leaderMW, "", onReminder)
	if err != nil {
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
		loop:     loop,
		leaderMW: leaderMW,
		router:   router,
	}, nil
}

// Push pushes a TurnInput into the Runner's TurnLoop buffer.
// Items are routed to the appropriate agent's loop by the source router.
// Returns (accepted, ack) where ack is non-nil only for preemptive pushes.
func (r *Runner) Push(item TurnInput, opts ...adk.PushOption[TurnInput, adk.Message]) (bool, <-chan struct{}) {
	return r.router.Push(item, opts...)
}

// Run starts the TurnLoop. It is non-blocking: the loop runs in the background.
// Use Wait to block until the loop exits.
func (r *Runner) Run(ctx context.Context) {
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
func (r *Runner) WaitContext(ctx context.Context) *adk.TurnLoopExitState[TurnInput, adk.Message] {
	state := r.loop.Wait()
	if r.leaderMW != nil {
		teamName := r.leaderMW.getTeamName()
		if teamName != "" {
			r.leaderMW.ShutdownAllTeammates(ctx)
		}
		// Stop the leader's own mailbox pump to prevent goroutine leak.
		// The pump is started by TeamCreate and is not covered by
		// ShutdownAllTeammates (which only handles teammate pumps).
		r.leaderMW.lifecycle.cleanupLeaderMailbox()
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
// This is the single factory used by both NewRunner (leader) and
// agentTool.buildTeammateAgent (teammate) to avoid duplicating the
// middleware-wiring logic.
func buildTeamAgent(ctx context.Context, conf *RunnerConfig, teamMW *teamMiddleware, extraInstruction string, onReminder func(ctx context.Context, agentName string, reminderText string)) (*adk.ChatModelAgent, plantask.Middleware, error) {
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

	handlers := append(defaultHandlers, stripPlantaskMiddleware(conf.AgentConfig.Handlers)...)

	newConfig := *conf.AgentConfig
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
// plantask.WithReminder. The zero value means "unset" and falls back to the
// default (10); only an explicitly negative value disables reminders. This
// prevents the common "Interval left unset" path from silently overwriting
// plantask's own default with 0 and turning reminders off.
func resolveReminderInterval(interval int) int {
	if interval == 0 {
		return defaultReminderInterval
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
			// Until TeamCreate runs, getTeamName() is empty and the resolved task
			// directory would collapse to {BaseDir}/tasks instead of the
			// team-scoped {BaseDir}/tasks/{teamName}. Reject task operations in
			// that window so tasks are never written outside a team and never
			// orphaned from the team that is eventually created.
			if mw.getTeamName() == "" {
				return fmt.Errorf("no active team; create one with %s before managing tasks", teamCreateToolName)
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
