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
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
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
	Mode         ConsumeMode
	Timeout      time.Duration
	CancelOpts   []CancelOption
	CheckPointID string
}

type ConsumeOption func(*consumeConfig)

// WithPreemptive sets the consume mode to preemptive, which cancels the
// currently running agent immediately.
func WithPreemptive() ConsumeOption {
	return func(config *consumeConfig) {
		config.Mode = ConsumePreemptive
	}
}

// WithPreemptiveOnTimeout sets the consume mode to preemptive with a timeout.
// If the current agent does not complete within the timeout, it will be canceled.
func WithPreemptiveOnTimeout(timeout time.Duration) ConsumeOption {
	return func(config *consumeConfig) {
		config.Mode = ConsumePreemptiveOnTimeout
		config.Timeout = timeout
	}
}

// WithCancelOptions appends cancel options to be used when canceling the agent.
func WithCancelOptions(opts ...CancelOption) ConsumeOption {
	return func(config *consumeConfig) {
		config.CancelOpts = append(config.CancelOpts, opts...)
	}
}

func WithConsumeCheckPointID(id string) ConsumeOption {
	return func(config *consumeConfig) {
		config.CheckPointID = id
	}
}

type ReceiveConfig struct {
	Timeout time.Duration
}

type MessageSource[T any] interface {
	Receive(context.Context, ReceiveConfig) (context.Context, T, []ConsumeOption, error)
	Front(context.Context, ReceiveConfig) (context.Context, T, []ConsumeOption, error)
}

type turnLoopRunConfig[T any] struct {
	checkPointID string
	item         T
}

// TurnLoopRunOption is an option for TurnLoop.Run.
type TurnLoopRunOption[T any] func(*turnLoopRunConfig[T])

func WithTurnLoopResume[T any](checkPointID string, item T) TurnLoopRunOption[T] {
	return func(c *turnLoopRunConfig[T]) {
		c.checkPointID = checkPointID
		c.item = item
	}
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
	// OnAgentEvents is called for each event emitted by the agent. Optional.
	// The inputItem is the message that triggered the current agent turn.
	// If not provided, the default implementation will consume all events and
	// return any error event encountered.
	OnAgentEvents func(ctx context.Context, inputItem T, event *AsyncIterator[*AgentEvent]) error
	// ReceiveTimeout is the timeout passed to Source.Receive on each iteration.
	// Zero means no timeout. Optional.
	ReceiveTimeout time.Duration

	Store CheckPointStore
}

// TurnLoop is a loop that pulls messages from a source, runs an Agent for
// each message, and dispatches resulting events. It supports preemptive
// cancellation when the source returns ConsumePreemptive and the current
// agent implements Cancellable.
type TurnLoop[T any] struct {
	source         MessageSource[T]
	genInput       func(ctx context.Context, item T) (*AgentInput, []AgentRunOption, error)
	getAgent       func(ctx context.Context, item T) (Agent, error)
	onAgentEvents  func(ctx context.Context, inputItem T, event *AsyncIterator[*AgentEvent]) error
	receiveTimeout time.Duration
	store          CheckPointStore
}

type turnLoopCancelSig struct {
	done   chan struct{}
	config atomic.Value
}

func newTurnLoopCancelSig() *turnLoopCancelSig {
	return &turnLoopCancelSig{
		done: make(chan struct{}),
	}
}

func (cs *turnLoopCancelSig) cancel(cfg *cancelConfig) {
	cs.config.Store(cfg)
	close(cs.done)
}

func (cs *turnLoopCancelSig) isCancelled() bool {
	select {
	case <-cs.done:
		return true
	default:
		return false
	}
}

func (cs *turnLoopCancelSig) getConfig() *cancelConfig {
	if v := cs.config.Load(); v != nil {
		return v.(*cancelConfig)
	}
	return nil
}

type turnLoopCancelSigKey struct{}

func withTurnLoopCancelSig(ctx context.Context, cs *turnLoopCancelSig) context.Context {
	return context.WithValue(ctx, turnLoopCancelSigKey{}, cs)
}

func getTurnLoopCancelSig(ctx context.Context) *turnLoopCancelSig {
	if v, ok := ctx.Value(turnLoopCancelSigKey{}).(*turnLoopCancelSig); ok {
		return v
	}
	return nil
}

// TurnLoopCancelFunc is the cancel function returned by WithCancel.
// Unlike Agent's CancelFunc, it does not require a context parameter
// since the context is already bound when WithCancel is called.
type TurnLoopCancelFunc func(opts ...CancelOption) error

// ErrAgentNotCancellableInTurnLoop is returned when WithCancel context is used
// but the Agent does not implement CancellableRun.
var ErrAgentNotCancellableInTurnLoop = errors.New("agent does not support cancel but WithCancel context was provided")

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

	onAgentEvents := config.OnAgentEvents
	if onAgentEvents == nil {
		onAgentEvents = func(_ context.Context, _ T, iter *AsyncIterator[*AgentEvent]) error {
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				if event.Err != nil {
					return event.Err
				}
			}
			return nil
		}
	}

	return &TurnLoop[T]{
		source:         config.Source,
		genInput:       config.GenInput,
		getAgent:       config.GetAgent,
		onAgentEvents:  onAgentEvents,
		receiveTimeout: config.ReceiveTimeout,
		store:          config.Store,
	}, nil
}

var ErrLoopExit = errors.New("loop exit")

// WithCancel returns a new context and a cancel function that can be used to
// cancel the TurnLoop's Run method externally. Each call to WithCancel creates
// an independent cancel signal, allowing multiple concurrent Run calls with
// separate cancel controls.
//
// The returned TurnLoopCancelFunc does not require a context parameter since
// the context is already bound when WithCancel is called.
//
// Example:
//
//	ctx, cancel := turnLoop.WithCancel(context.Background())
//	go func() {
//	    err := turnLoop.Run(ctx)
//	}()
//	// Later, to cancel:
//	cancel(adk.WithCancelMode(adk.CancelAfterToolCall))
func (l *TurnLoop[T]) WithCancel(ctx context.Context) (context.Context, TurnLoopCancelFunc) {
	cs := newTurnLoopCancelSig()
	ctx = withTurnLoopCancelSig(ctx, cs)

	var once sync.Once
	cancelFn := func(opts ...CancelOption) error {
		cfg := &cancelConfig{
			Mode: CancelImmediate,
		}
		for _, opt := range opts {
			opt(cfg)
		}

		cancelled := false
		once.Do(func() {
			cs.cancel(cfg)
			cancelled = true
		})

		if !cancelled {
			return ErrAgentFinished
		}
		return nil
	}

	return ctx, cancelFn
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
//
// To enable external cancellation, use WithCancel to create a cancellable context:
//
//	ctx, cancel := turnLoop.WithCancel(context.Background())
//	go turnLoop.Run(ctx)
//	// Later: cancel()
//
// To enable checkpoint-based resumption, use WithTurnLoopResume:
//
//	err := turnLoop.Run(ctx, WithTurnLoopResume("session-123"))
func (l *TurnLoop[T]) Run(ctx context.Context, opts ...TurnLoopRunOption[T]) error {
	var runCfg turnLoopRunConfig[T]
	for _, opt := range opts {
		opt(&runCfg)
	}

	cs := getTurnLoopCancelSig(ctx)
	toResumeFirst := false
	if len(runCfg.checkPointID) > 0 {
		toResumeFirst = true
	}

	for {
		if cs != nil && cs.isCancelled() {
			return nil
		}

		var nCtx context.Context
		var item T
		var checkPointID string
		if !toResumeFirst {
			var err error
			var option []ConsumeOption
			nCtx, item, option, err = l.source.Receive(ctx, ReceiveConfig{
				Timeout: l.receiveTimeout,
			})
			if errors.Is(err, ErrLoopExit) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("failed to receive message: %w", err)
			}
			o := applyConsumeOptions(option)
			checkPointID = o.CheckPointID
		} else {
			nCtx = ctx
			item = runCfg.item
			checkPointID = runCfg.checkPointID
		}

		if len(checkPointID) > 0 && l.store == nil {
			return fmt.Errorf("CheckPointStore is required")
		}

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
		_, isAgentCancellable := agent.(CancellableRun)
		if cs != nil && !isAgentCancellable {
			return fmt.Errorf("%w: agent %s", ErrAgentNotCancellableInTurnLoop, agent.Name(nCtx))
		}

		if toResumeFirst {
			var err error
			iter, cancelFunc, err = NewRunner(nCtx, RunnerConfig{
				EnableStreaming: input.EnableStreaming,
				Agent:           agent,
				CheckPointStore: l.store,
			}).ResumeWithCancel(nCtx, checkPointID, runOpts...)
			if err != nil {
				return fmt.Errorf("failed to resume agent: %w", err)
			}
			toResumeFirst = false
		} else if isAgentCancellable {
			var cps CheckPointStore
			if len(checkPointID) > 0 {
				cps = l.store
				runOpts = append(runOpts, WithCheckPointID(checkPointID))
			}
			iter, cancelFunc = NewRunner(nCtx, RunnerConfig{
				EnableStreaming: input.EnableStreaming,
				Agent:           agent,
				CheckPointStore: cps,
			}).RunWithCancel(nCtx, input.Messages, runOpts...)
		} else {
			var cps CheckPointStore
			if len(checkPointID) > 0 {
				cps = l.store
				runOpts = append(runOpts, WithCheckPointID(checkPointID))
			}
			iter = NewRunner(nCtx, RunnerConfig{
				EnableStreaming: input.EnableStreaming,
				Agent:           agent,
				CheckPointStore: cps,
			}).Run(nCtx, input.Messages, runOpts...)
		}

		handleEvents := func() error {
			return l.handleEvents(ctx, item, iter, checkPointID)
		}

		var handleEventErr error
		if cancelFunc != nil {
			done := make(chan struct{})

			go func() {
				defer func() {
					panicErr := recover()
					if panicErr != nil {
						handleEventErr = safe.NewPanicErr(panicErr, debug.Stack())
					}

					close(done)
				}()

				handleEventErr = handleEvents()
			}()

			frontDone := make(chan struct{})
			var frontErr error
			var option []ConsumeOption
			go func() {
				defer func() {
					panicErr := recover()
					if panicErr != nil {
						frontErr = safe.NewPanicErr(panicErr, debug.Stack())
					}

					close(frontDone)
				}()
				_, _, option, frontErr = l.source.Front(nCtx, ReceiveConfig{
					Timeout: l.receiveTimeout,
				})
			}()

			var externalCancelled bool
			select {
			case <-frontDone:
			case <-done:
			case <-func() <-chan struct{} {
				if cs != nil {
					return cs.done
				}
				return nil
			}():
				externalCancelled = true
				cfg := cs.getConfig()
				err := cancelFunc(nCtx, cancelConfigToOpts(cfg)...)
				if err != nil && !errors.Is(err, ErrAgentFinished) {
					<-done
					return fmt.Errorf("failed to cancel agent: %w", err)
				}
			}

			if externalCancelled {
				<-done
				return l.wrapHandleEventErr(handleEventErr)
			}

			if frontErr != nil {
				<-done
				if errors.Is(frontErr, ErrLoopExit) {
					return nil
				}
				return fmt.Errorf("failed to front message: %w", frontErr)
			}

			o := applyConsumeOptions(option)
			switch o.Mode {
			case ConsumePreemptive:
				err := cancelFunc(nCtx, o.CancelOpts...)
				if err != nil {
					<-done
					return fmt.Errorf("failed to cancel agent: %w", err)
				}
			case ConsumePreemptiveOnTimeout:
				select {
				case <-done:
				case <-time.After(o.Timeout):
					err := cancelFunc(nCtx, o.CancelOpts...)
					if err != nil {
						<-done
						return fmt.Errorf("failed to cancel agent: %w", err)
					}
				case <-func() <-chan struct{} {
					if cs != nil {
						return cs.done
					}
					return nil
				}():
					cfg := cs.getConfig()
					err := cancelFunc(nCtx, cancelConfigToOpts(cfg)...)
					if err != nil && !errors.Is(err, ErrAgentFinished) {
						<-done
						return fmt.Errorf("failed to cancel agent: %w", err)
					}
					<-done
					return l.wrapHandleEventErr(handleEventErr)
				}
			}

			<-done
			if err := l.wrapHandleEventErr(handleEventErr); err != nil {
				return err
			}
		} else {
			if handleEventErr = handleEvents(); handleEventErr != nil {
				if err := l.wrapHandleEventErr(handleEventErr); err != nil {
					return err
				}
			}
		}
	}
}

func (l *TurnLoop[T]) wrapHandleEventErr(handleEventErr error) error {
	if handleEventErr == nil {
		return nil
	}
	if errors.Is(handleEventErr, ErrLoopExit) {
		return nil
	}
	var interruptErr *TurnLoopInterruptError[T]
	if errors.As(handleEventErr, &interruptErr) {
		return interruptErr
	}
	return fmt.Errorf("failed to handle events: %w", handleEventErr)
}

func (l *TurnLoop[T]) handleEvents(ctx context.Context, item T, iter *AsyncIterator[*AgentEvent], checkPointID string) error {
	copies := copyEventIterator(iter, 2)
	oe := l.onAgentEvents(ctx, item, copies[0])
	if oe != nil {
		return oe
	}
	for {
		e, ok := copies[1].Next()
		if !ok {
			break
		}
		if e.Action != nil && e.Action.Interrupted != nil {
			return &TurnLoopInterruptError[T]{
				Item:              item,
				CheckpointID:      checkPointID,
				InterruptContexts: e.Action.Interrupted.InterruptContexts,
			}
		}
	}
	return nil
}

func cancelConfigToOpts(cfg *cancelConfig) []CancelOption {
	if cfg == nil {
		return nil
	}
	opts := []CancelOption{WithCancelMode(cfg.Mode)}
	if cfg.Timeout != nil {
		opts = append(opts, WithCancelTimeout(*cfg.Timeout))
	}
	return opts
}

func applyConsumeOptions(opts []ConsumeOption) *consumeConfig {
	var config consumeConfig
	for _, opt := range opts {
		opt(&config)
	}
	return &config
}

type TurnLoopInterruptError[T any] struct {
	Item         T
	CheckpointID string
	// InterruptContexts provides a structured, user-facing view of the interrupt chain.
	// Each context represents a step in the agent hierarchy that was interrupted.
	InterruptContexts []*InterruptCtx
}

func (t *TurnLoopInterruptError[T]) Error() string {
	return fmt.Sprintf("TurnLoopInterruptError[%s]: %v", t.CheckpointID, t.InterruptContexts)
}
