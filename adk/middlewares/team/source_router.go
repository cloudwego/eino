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

package team

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// sourceRouter demultiplexes a single upstream MessageSource into per-agent sources.
//
// It is pull-based (no background goroutine). When an agent's source calls Receive,
// the router reads from upstream until it finds a message for that agent.
// Messages for other agents are buffered in their per-agent queues.
// Messages with an empty or unknown TargetAgent are delivered to the default agent (leader).
type sourceRouter struct {
	upstream     adk.MessageSource[TurnInput]
	defaultAgent string

	mu        sync.Mutex // protects buffers, closed, and mailboxes
	readMu    sync.Mutex // serializes upstream reads (only one reader at a time)
	buffers   map[string][]TurnInput
	mailboxes map[string]*MailboxMessageSource // dynamically attached mailboxes
	closed    bool
}

// newSourceRouter creates a pull-based sourceRouter.
func newSourceRouter(upstream adk.MessageSource[TurnInput], defaultAgent string) *sourceRouter {
	return &sourceRouter{
		upstream:     upstream,
		defaultAgent: defaultAgent,
		buffers:      make(map[string][]TurnInput),
		mailboxes:    make(map[string]*MailboxMessageSource),
	}
}

// SourceFor returns a MessageSource for the given agent.
// Messages from upstream with matching TargetAgent are delivered to this source.
// If a mailbox has been registered via SetMailbox, it will be checked (non-blocking)
// before reading from the upstream router.
func (r *sourceRouter) SourceFor(agentName string) adk.MessageSource[TurnInput] {
	r.mu.Lock()
	if _, ok := r.buffers[agentName]; !ok {
		r.buffers[agentName] = nil
	}
	r.mu.Unlock()

	return &routerAgentSource{router: r, agentName: agentName}
}

// SetMailbox dynamically attaches a MailboxMessageSource for the given agent.
// This allows the leader's mailbox to be created after TeamCreate provides
// the teamName at runtime.
func (r *sourceRouter) SetMailbox(agentName string, ms *MailboxMessageSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mailboxes[agentName] = ms
}

// UnsetMailbox detaches the mailbox for the given agent.
func (r *sourceRouter) UnsetMailbox(agentName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.mailboxes, agentName)
}

// getMailbox returns the mailbox for the given agent, or nil if none is set.
func (r *sourceRouter) getMailbox(agentName string) *MailboxMessageSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mailboxes[agentName]
}

func (r *sourceRouter) enqueueFor(agentName string, item TurnInput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enqueueForLocked(agentName, item)
}

func (r *sourceRouter) enqueueForLocked(agentName string, item TurnInput) {
	if _, ok := r.buffers[agentName]; !ok {
		r.buffers[agentName] = nil
	}
	r.buffers[agentName] = append(r.buffers[agentName], item)
}

func (r *sourceRouter) popBufferedOrClosedLocked(agentName string) (TurnInput, bool, error) {
	if buf := r.buffers[agentName]; len(buf) > 0 {
		item := buf[0]
		r.buffers[agentName] = buf[1:]
		return item, true, nil
	}
	if r.closed {
		return TurnInput{}, false, adk.ErrLoopExit
	}
	return TurnInput{}, false, nil
}

func (r *sourceRouter) routeItemLocked(item TurnInput) {
	target := item.TargetAgent
	if target == "" {
		target = r.defaultAgent
	}

	if _, ok := r.buffers[target]; ok {
		r.buffers[target] = append(r.buffers[target], item)
		return
	}

	r.enqueueForLocked(r.defaultAgent, item)
}

// receiveFor reads messages for the given agent.
// It checks the buffer first, then reads from upstream until it finds a matching message.
func (r *sourceRouter) receiveFor(ctx context.Context, name string) (context.Context, TurnInput, []adk.ConsumeOption, error) {
	for {
		// 1. Fast path: check buffer.
		r.mu.Lock()
		item, ok, err := r.popBufferedOrClosedLocked(name)
		if ok || err != nil {
			r.mu.Unlock()
			return ctx, item, nil, err
		}
		r.mu.Unlock()

		// 2. Slow path: read from upstream (serialized).
		r.readMu.Lock()

		// Re-check buffer — another reader may have filled it while we waited.
		r.mu.Lock()
		item, ok, err = r.popBufferedOrClosedLocked(name)
		if ok || err != nil {
			r.mu.Unlock()
			r.readMu.Unlock()
			return ctx, item, nil, err
		}
		r.mu.Unlock()

		// Read one message from upstream.
		_, item, _, err = r.upstream.Receive(ctx, adk.ReceiveConfig{})
		r.readMu.Unlock()

		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ctx, TurnInput{}, nil, err
			}
			r.mu.Lock()
			r.closed = true
			r.mu.Unlock()
			return ctx, TurnInput{}, nil, err
		}

		// 3. Route the message.
		target := item.TargetAgent
		if target == "" {
			target = r.defaultAgent
		}

		// If it's for us, return immediately.
		if target == name {
			return ctx, item, nil, nil
		}

		// Buffer for the target agent (or default if unknown).
		r.mu.Lock()
		r.routeItemLocked(item)
		r.mu.Unlock()
		// Loop back — try buffer again or read next message.
	}
}

// routerAgentSource is a per-agent MessageSource backed by the sourceRouter.
// On Receive, the agent's mailbox (if registered via SetMailbox) is checked
// first (non-blocking). If no mailbox messages, falls through to the router.
type routerAgentSource struct {
	router    *sourceRouter
	agentName string

	mu      sync.Mutex
	peeked  *TurnInput
	peedOpt []adk.ConsumeOption
}

func (s *routerAgentSource) Receive(ctx context.Context,
	cfg adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {
	s.mu.Lock()
	if s.peeked != nil {
		item := *s.peeked
		opts := s.peedOpt
		s.peeked = nil
		s.peedOpt = nil
		s.mu.Unlock()
		log.Printf("Receive[%s]: %v <nil> (from peeked)", s.agentName, item)
		return ctx, item, opts, nil
	}
	s.mu.Unlock()

	ctx, item, opts, err := s.receive(ctx, cfg, true)
	log.Printf("Receive[%s]: %v %v", s.agentName, item, err)
	return ctx, item, opts, err
}

func (s *routerAgentSource) Front(ctx context.Context,
	cfg adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {
	ctx, item, opts, err := s.receive(ctx, cfg, false)
	if err != nil {
		return ctx, item, opts, err
	}
	s.mu.Lock()
	s.peeked = &item
	s.peedOpt = opts
	s.mu.Unlock()
	return ctx, item, opts, nil
}

func (s *routerAgentSource) receive(ctx context.Context,
	cfg adk.ReceiveConfig, notifyIdle bool) (context.Context, TurnInput, []adk.ConsumeOption, error) {

	if mailbox := s.router.getMailbox(s.agentName); mailbox != nil {
		// Try non-blocking read first.
		item, ok, err := mailbox.tryReceive(ctx, notifyIdle)
		if err != nil {
			return ctx, TurnInput{TargetAgent: s.agentName}, nil, err
		}
		if ok {
			item.TargetAgent = s.agentName
			return ctx, item, nil, nil
		}

		return s.receiveFromMailboxOrUpstream(ctx, cfg, mailbox)
	}

	// No mailbox registered yet (e.g. leader before TeamCreate) — fall through
	// to the upstream router which blocks until it has a message for this agent.
	return s.router.receiveFor(ctx, s.agentName)
}

type receiveResult struct {
	source string
	item   TurnInput
	opts   []adk.ConsumeOption
	err    error
}

func (s *routerAgentSource) receiveFromMailboxOrUpstream(ctx context.Context,
	cfg adk.ReceiveConfig, mailbox *MailboxMessageSource) (context.Context, TurnInput, []adk.ConsumeOption, error) {

	// Use an independent context (not derived from ctx) to avoid internal cancellation
	// (when one source wins the race) from propagating "context canceled" errors upstream.
	childCtx, cancel := context.WithCancel(context.Background())
	results := make(chan receiveResult, 2)

	safeGo(func() {
		_, item, opts, err := mailbox.Receive(childCtx, cfg)
		results <- receiveResult{
			source: "mailbox",
			item:   item,
			opts:   opts,
			err:    err,
		}
	})

	safeGo(func() {
		_, item, opts, err := s.router.receiveFor(childCtx, s.agentName)
		results <- receiveResult{
			source: "upstream",
			item:   item,
			opts:   opts,
			err:    err,
		}
	})

	select {
	case first := <-results:
		cancel()

		safeGo(func() {
			second := <-results
			if second.err == nil {
				second.item.TargetAgent = s.agentName
				s.router.enqueueFor(s.agentName, second.item)
			}
		})

		if first.err != nil {
			return ctx, TurnInput{}, nil, first.err
		}

		first.item.TargetAgent = s.agentName
		// Return the original ctx (not childCtx) so subsequent calls don't inherit
		// the already-cancelled internal context.
		return ctx, first.item, first.opts, nil
	case <-ctx.Done():
		cancel()
		return ctx, TurnInput{}, nil, ctx.Err()
	}
}
