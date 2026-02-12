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
	"time"
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
)

// ConsumeOption describes how a received message should be consumed.
// It combines ConsumeMode (preemptive vs queued) with CancelMode
// (how to cancel the running agent when preempting).
type ConsumeOption struct {
	// Mode specifies whether the message should preempt the current agent
	// or be queued. Default zero value is ConsumeNonPreemptive.
	Mode ConsumeMode
	// CancelOption specifies when and how the running agent should be canceled.
	// Only meaningful when Mode is ConsumePreemptive and the agent
	// implements Cancellable. Default zero value means CancelImmediate.
	CancelOption CancelOption
}

// NonPreemptiveConsumeOption is a convenience value for the common
// non-preemptive (queued) case.
var NonPreemptiveConsumeOption = ConsumeOption{Mode: ConsumeNonPreemptive}

// MessageSource is an interface for pulling typed messages from an external source.
// Receive blocks until a message is available or an error occurs.
// The timeout parameter specifies the maximum duration to wait for a message.
// The returned ConsumeOption indicates whether the message should preempt the
// currently running agent (and how to cancel it) or be queued for processing
// after it finishes.
type MessageSource[T any] interface {
	Receive(ctx context.Context, timeout time.Duration) (T, ConsumeOption, error)
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

// recvResult holds the result of a concurrent Receive call.
type recvResult[T any] struct {
	item   T
	option ConsumeOption
	err    error
}

// iterResult holds the result of a single AsyncIterator.Next call.
type iterResult struct {
	event *AgentEvent
	ok    bool
}

// Run starts the blocking loop that continuously receives messages, runs
// agents, and dispatches events. While an agent is running, the next message
// is received concurrently. If that message's ConsumeOption has ConsumePreemptive
// mode and the running agent implements Cancellable, the agent is canceled
// (using the CancelMode from the option) and the new message is processed
// immediately.
func (l *TurnLoop[T]) Run(ctx context.Context) error {
	// done is closed when Run returns, signaling background goroutines to exit.
	done := make(chan struct{})
	defer close(done)

	// Initial blocking receive — no agent running yet, mode is irrelevant.
	item, _, err := l.source.Receive(ctx, l.receiveTimeout)
	if err != nil {
		return err
	}

	for {
		input, runOpts, e := l.genInput(ctx, item)
		if e != nil {
			return fmt.Errorf("failed to generate agent input: %w", e)
		}

		agent, e := l.getAgent(ctx, item)
		if e != nil {
			return fmt.Errorf("failed to get agent: %w", e)
		}

		// Start receiving the next message concurrently with agent execution.
		// The channel is buffered so the goroutine never blocks on send.
		recvCh := make(chan recvResult[T], 1)
		go func() {
			i, opt, e_ := l.source.Receive(ctx, l.receiveTimeout)
			recvCh <- recvResult[T]{i, opt, e_}
		}()

		// Run the agent and forward events through a channel so we can
		// select between agent events and incoming messages.
		iter := agent.Run(ctx, input, runOpts...)
		eventCh := make(chan iterResult, 1)
		go func() {
			for {
				event, ok := iter.Next()
				select {
				case eventCh <- iterResult{event, ok}:
				case <-done:
					return
				}
				if !ok {
					return
				}
			}
		}()

		var pending *recvResult[T]
		var turnErr error

	eventLoop:
		for {
			select {
			case ev := <-eventCh:
				if !ev.ok {
					break eventLoop
				}
				if ev.event.Err != nil {
					turnErr = fmt.Errorf("agent run failed: %w", ev.event.Err)
					break eventLoop
				}
				if l.onAgentEvent != nil {
					if e_ := l.onAgentEvent(ctx, item, ev.event); e_ != nil {
						turnErr = fmt.Errorf("OnAgentEvent failed: %w", e_)
						break eventLoop
					}
				}

			case recv := <-recvCh:
				recvCh = nil // nil channel never matches in select
				if recv.option.Mode == ConsumePreemptive && recv.err == nil {
					if ca, ok := agent.(Cancellable); ok {
						if e_ := ca.Cancel(ctx, recv.option.CancelOption); e_ != nil {
							return fmt.Errorf("failed to cancel agent: %w", e_)
						}
						// Drain remaining events after cancellation.
						for {
							ev := <-eventCh
							if !ev.ok {
								break
							}
						}
						pending = &recvResult[T]{item: recv.item}
						break eventLoop
					}
				}
				// Non-preemptive, preemptive but not Cancellable, or
				// source error: buffer and let the eventLoop finish
				// processing the current agent's events first.
				pending = &recvResult[T]{item: recv.item, err: recv.err}
			}
		}

		if turnErr != nil {
			return turnErr
		}

		if pending != nil {
			if pending.err != nil {
				return pending.err
			}
			item = pending.item
		} else {
			// Agent finished before the next message arrived; wait for it.
			recv := <-recvCh
			if recv.err != nil {
				return recv.err
			}
			item = recv.item
		}
	}
}
