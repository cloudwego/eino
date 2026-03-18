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

// adk/middlewares/team/team_runner.go

package team

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

// RunnerConfig configures a Runner.
type RunnerConfig struct {
	// AgentConfig is the configuration for the agent. Required.
	// NewRunner automatically prepends the team leader middleware to Handlers.
	AgentConfig *adk.ChatModelAgentConfig

	// TeamConfig contains team-specific settings (Backend, BaseDir, Model). Required.
	TeamConfig *Config

	// Source provides messages to drive the TurnLoop. Required.
	Source adk.MessageSource[TurnInput]

	// GenInput converts a TurnInput into AgentInput for the targeted agent.
	// Use this to manage conversation history (e.g., pre-load history after
	// a restart). Required.
	GenInput func(ctx context.Context, input TurnInput) (*adk.AgentInput, []adk.AgentRunOption, error)

	// OnAgentEvents receives the full event iterator along with the agent name,
	// allowing the caller to consume events at their own pace (e.g., streaming,
	// early termination). The iterator is wrapped to transparently maintain
	// per-agent conversation history — callers need not track messages themselves.
	// Required.
	OnAgentEvents func(ctx context.Context, inputItem TurnInput, events *adk.AsyncIterator[*adk.AgentEvent]) error
}

// Runner wraps the TurnLoop lifecycle with multi-agent routing
// and per-agent conversation history management.
type Runner struct {
	loop     *adk.TurnLoop[TurnInput]
	leaderMW *teamMiddleware
}

// NewRunner creates a new Runner with multi-agent routing support.
// It creates the team leader middleware, prepends it to AgentConfig.Handlers,
// constructs the ChatModelAgent, and wires up the TurnLoop.
func NewRunner(ctx context.Context, conf *RunnerConfig) (*Runner, error) {
	if conf.AgentConfig == nil {
		return nil, fmt.Errorf("AgentConfig is required")
	}
	if conf.TeamConfig == nil {
		return nil, fmt.Errorf("TeamConfig is required")
	}
	if conf.Source == nil {
		return nil, fmt.Errorf("Source is required")
	}
	if conf.OnAgentEvents == nil {
		return nil, fmt.Errorf("OnAgentEvents is required")
	}
	if conf.GenInput == nil {
		return nil, fmt.Errorf("GenInput is required")
	}

	// Ensure shared inbox lock manager exists.
	if conf.TeamConfig.InboxLocks == nil {
		conf.TeamConfig.InboxLocks = newInboxLockManager()
	}

	leaderMW := newTeamLeadMiddleware(conf)

	router := newSourceRouter(conf.Source, LeaderAgentName)
	leaderMW.router = router

	// Always inject team-aware plantask middleware, removing any user-provided one
	// (which lacks team resolvers/hooks).
	defaultHandlers := []adk.ChatModelAgentMiddleware{leaderMW}
	ptMW, err := plantask.New(ctx, &plantask.Config{
		Backend: conf.TeamConfig.Backend,
		BaseDir: conf.TeamConfig.BaseDir,
	},
		plantask.WithTaskAssignedHook(newTaskAssignedNotifier(conf.TeamConfig, func() string { return leaderMW.teamName })),
		plantask.WithTaskBaseDirResolver(func(_ context.Context) string {
			return tasksDirPath(conf.TeamConfig.BaseDir, leaderMW.teamName)
		}),
		plantask.WithAgentNameResolver(func(_ context.Context) string { return leaderMW.agentName }),
	)
	if err != nil {
		return nil, fmt.Errorf("create plantask middleware: %w", err)
	}
	defaultHandlers = append(defaultHandlers, ptMW)
	leaderMW.ptMW = ptMW.(plantask.Middleware)

	handlers := append(defaultHandlers, stripPlantaskMiddleware(conf.AgentConfig.Handlers)...)

	newConfig := *conf.AgentConfig
	newConfig.Handlers = handlers

	agent, err := adk.NewChatModelAgent(ctx, &newConfig)
	if err != nil {
		return nil, fmt.Errorf("create leader agent: %w", err)
	}

	loop, err := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		Source:   router.SourceFor(LeaderAgentName),
		GenInput: conf.GenInput,
		GetAgent: func(_ context.Context, _ TurnInput) (adk.Agent, error) {
			return agent, nil
		},
		OnAgentEvents: conf.OnAgentEvents,
	})
	if err != nil {
		return nil, fmt.Errorf("create turn loop: %w", err)
	}

	return &Runner{
		loop:     loop,
		leaderMW: leaderMW,
	}, nil
}

// Run starts the TurnLoop. It blocks until the loop exits.
func (r *Runner) Run(ctx context.Context) error {
	if r.leaderMW != nil {
		defer func() {
			teamName := r.leaderMW.teamName
			if teamName != "" {
				// Cleanup should still run after the serving context is cancelled.
				r.leaderMW.ShutdownAllTeammates(context.Background(), teamName)
			}
		}()
	}

	return r.loop.Run(ctx)
}

// WithCancel returns a new context and a CancelFunc that can be used to
// cancel the Runner's underlying TurnLoop externally.
// The returned context should be passed to Run.
func (r *Runner) WithCancel(ctx context.Context) (context.Context, adk.CancelFunc) {
	return r.loop.WithCancel(ctx)
}

// newTeammateRunner creates a minimal Runner for a teammate.
// The teammate's source checks mailbox (non-blocking) first, then router (upstream).
func newTeammateRunner(conf *RunnerConfig, router *sourceRouter,
	agent *adk.ChatModelAgent, agentName, teamName string) (*Runner, error) {

	tmMailbox := newMailboxFromConfig(conf.TeamConfig, teamName, agentName)

	mailboxSource := NewMailboxMessageSource(tmMailbox, &MailboxSourceConfig{
		OwnerName: agentName,
		Role:      teamRoleTeammate,
	})

	router.SetMailbox(agentName, mailboxSource)
	tmSource := router.SourceFor(agentName)

	loop, err := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		Source:   tmSource,
		GenInput: conf.GenInput,
		GetAgent: func(_ context.Context, _ TurnInput) (adk.Agent, error) {
			return agent, nil
		},
		OnAgentEvents: conf.OnAgentEvents,
	})
	if err != nil {
		return nil, fmt.Errorf("create teammate turn loop: %w", err)
	}

	return &Runner{
		loop: loop,
	}, nil
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
