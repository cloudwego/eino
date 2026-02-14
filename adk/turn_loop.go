/*
 * Copyright 2025 CloudWeGo Authors
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

package adk

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/cloudwego/eino/internal/safe"
)

// ConsumeMode specifies how a received message should be consumed
// relative to the currently running agent.
type ConsumeMode int

const (
	// ConsumeNonPreemptive processes the message after the current agent
	// finishes. This is the default queued behavior.
	ConsumeNonPreemptive ConsumeMode = iota
	// ConsumePreemptive cancels the currently running agent (if it
	// implements Cancellable) and processes the message immediately.
	// If the agent does not implement Cancellable, the message is
	// buffered and processed after the agent finishes.
	ConsumePreemptive

	ConsumePreemptiveOnTimeout
)

type consumeConfig struct {
	Mode       ConsumeMode
	Timeout    time.Duration
	CancelOpts []CancelOption
}

type ConsumeOption func(*consumeConfig)

func WithPreemptive() ConsumeOption {
	return func(config *consumeConfig) {
		config.Mode = ConsumePreemptive
	}
}

func WithPreemptiveOnTimeout(timeout time.Duration) ConsumeOption {
	return func(config *consumeConfig) {
		config.Mode = ConsumePreemptiveOnTimeout
		config.Timeout = timeout
	}
}

func WithCancelOptions(opts ...CancelOption) ConsumeOption {
	return func(config *consumeConfig) {
		config.CancelOpts = append(config.CancelOpts, opts...)
	}
}

type ReceiveConfig struct {
	Timeout time.Duration
}

type MessageSource[T any] interface {
	Receive(context.Context, ReceiveConfig) (context.Context, T, []ConsumeOption, error)
}

// TurnLoopConfig is the configuration for creating a TurnLoop.
type TurnLoopConfig[T any] struct {
	// Source provides messages to drive the loop. Required.
	Source MessageSource[T]
	// GenInput converts a received message into AgentInput and optional
	// RunOptions for the agent. Required.
	GenInput func(ctx context.Context, item T) (*AgentInput, []AgentRunOption, error)
	// GetAgent returns the Agent to run for a given message. Required.
	GetAgent func(ctx context.Context, item T) (Agent, error)
	// OnAgentEvent is called for each event emitted by the agent. Optional.
	// The inputItem is the message that triggered the current agent turn.
	OnAgentEvent func(ctx context.Context, inputItem T, event *AgentEvent) error
	// ReceiveTimeout is the timeout passed to Source.Receive on each iteration.
	// Zero means no timeout. Optional.
	ReceiveTimeout time.Duration
}

// TurnLoop is a loop that pulls messages from a source, runs an Agent for
// each message, and dispatches resulting events. It supports preemptive
// cancellation when the source returns ConsumePreemptive and the current
// agent implements Cancellable.
type TurnLoop[T any] struct {
	source         MessageSource[T]
	genInput       func(ctx context.Context, item T) (*AgentInput, []AgentRunOption, error)
	getAgent       func(ctx context.Context, item T) (Agent, error)
	onAgentEvent   func(ctx context.Context, inputItem T, event *AgentEvent) error
	receiveTimeout time.Duration
}

// NewTurnLoop creates a new TurnLoop from the given configuration.
// Source, GenInput, and GetAgent are required fields.
func NewTurnLoop[T any](config TurnLoopConfig[T]) (*TurnLoop[T], error) {
	if config.Source == nil {
		return nil, fmt.Errorf("TurnLoopConfig.Source is required")
	}
	if config.GenInput == nil {
		return nil, fmt.Errorf("TurnLoopConfig.GenInput is required")
	}
	if config.GetAgent == nil {
		return nil, fmt.Errorf("TurnLoopConfig.GetAgent is required")
	}

	return &TurnLoop[T]{
		source:         config.Source,
		genInput:       config.GenInput,
		getAgent:       config.GetAgent,
		onAgentEvent:   config.OnAgentEvent,
		receiveTimeout: config.ReceiveTimeout,
	}, nil
}

// Run starts the blocking loop that continuously receives messages from the
// source, runs the agent returned by GetAgent for each message, and dispatches
// resulting events to OnAgentEvent. It blocks until the source returns an error
// (including context cancellation) or a callback fails.
//
// If a received message has ConsumePreemptive mode and the current agent
// implements Cancellable, the agent is canceled and the new message is processed
// immediately. If the agent does not implement Cancellable, preemptive messages
// are queued and processed after the current agent finishes.
func (l *TurnLoop[T]) Run(ctx context.Context) error {
	// Initial blocking receive — no agent is running yet.
	nCtx, item, option, err := l.source.Receive(ctx, ReceiveConfig{
		Timeout: l.receiveTimeout,
	})
	if err != nil {
		return fmt.Errorf("failed to receive message: %w", err)
	}

	for {
		input, runOpts, e := l.genInput(nCtx, item)
		if e != nil {
			return fmt.Errorf("failed to generate agent input: %w", e)
		}

		agent, e := l.getAgent(nCtx, item)
		if e != nil {
			return fmt.Errorf("failed to get agent: %w", e)
		}

		var cancelFunc CancelFunc
		var iter *AsyncIterator[*AgentEvent]
		if ca, isAgentCancellable := agent.(CancellableRun); isAgentCancellable {
			iter, cancelFunc = ca.RunWithCancel(nCtx, input, runOpts...)
		} else {
			iter = agent.Run(nCtx, input, runOpts...)
		}

		// handleEvents drains the agent iterator, forwarding each event to the
		// OnAgentEvent callback. It is called directly in the non-cancellable
		// path and from a goroutine in the cancellable path.
		handleEvents := func() error {
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}

				if event.Err != nil {
					return fmt.Errorf("agent run failed: %w", event.Err)
				}

				if l.onAgentEvent != nil {
					e = l.onAgentEvent(ctx, item, event)
					if e != nil {
						return fmt.Errorf("OnAgentEvent callback failed: %w", e)
					}
				}
			}

			return nil
		}

		var handleEventErr error
		if cancelFunc != nil {
			// Cancellable path: consume events in a goroutine so the main
			// goroutine can block on Receive concurrently.
			done := make(chan struct{})

			go func() {
				defer func() {
					// Recover panics from the iterator or callback so they
					// don't crash the process; surface them as errors instead.
					panicErr := recover()
					if panicErr != nil {
						handleEventErr = safe.NewPanicErr(panicErr, debug.Stack())
					}

					close(done)
				}()

				handleEventErr = handleEvents()
			}()

			// Block on the next message while events are being consumed above.
			nCtx, item, option, err = l.source.Receive(nCtx, ReceiveConfig{
				Timeout: l.receiveTimeout,
			})
			if err != nil {
				<-done // wait for the event goroutine before returning
				return fmt.Errorf("failed to receive message: %w", err)
			}

			// If the new message requests preemption, cancel the running agent.
			// Cancel triggers the iterator to terminate, which unblocks the
			// event goroutine above.
			o := applyConsumeOptions(option)
			switch o.Mode {
			case ConsumePreemptive:
				err = cancelFunc(nCtx, o.CancelOpts...)
				if err != nil {
					<-done // wait for the event goroutine before returning
					return fmt.Errorf("failed to cancel agent: %w", err)
				}
			case ConsumePreemptiveOnTimeout:
				select {
				case <-done:
				case <-time.After(o.Timeout):
					err = cancelFunc(nCtx, o.CancelOpts...)
					if err != nil {
						<-done // wait for the event goroutine before returning
						return fmt.Errorf("failed to cancel agent: %w", err)
					}
				}
			}

			// Wait for event consumption to finish (normal completion or
			// post-cancel drain) before starting the next turn.
			<-done
			if handleEventErr != nil {
				return fmt.Errorf("failed to handle events: %w", handleEventErr)
			}
		} else {
			// Non-cancellable path: consume all events sequentially, then
			// block on the next message.
			if handleEventErr = handleEvents(); handleEventErr != nil {
				return fmt.Errorf("failed to handle events: %w", handleEventErr)
			}

			nCtx, item, option, err = l.source.Receive(nCtx, ReceiveConfig{
				Timeout: l.receiveTimeout,
			})
			if err != nil {
				return fmt.Errorf("failed to receive message: %w", err)
			}
		}
	}
}

func applyConsumeOptions(opts []ConsumeOption) *consumeConfig {
	var config consumeConfig
	for _, opt := range opts {
		opt(&config)
	}
	return &config
}
