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

package subagent

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/task"
)

const (
	// ResumeInputKind carries JSON resume targets for a Runner interrupt.
	ResumeInputKind = "eino.subagent.resume"
)

// StartRequest starts one finite task in a persistent child session.
type StartRequest[M adk.MessageType] struct {
	InvocationID    string
	ParentSessionID string
	ChildSessionID  string
	AgentName       string
	Description     string
	Input           *adk.TypedAgentInput[M]
	StartMode       task.StartMode
	OnEvent         func(*adk.TypedAgentEvent[M])
}

// Handle identifies one finite task and its persistent child session.
type Handle struct {
	taskID         string
	childSessionID string
	sendInput      func(context.Context, *task.Input) error
	wait           func(context.Context) (*task.Outcome, error)
	cancel         func(context.Context, string) error
}

// ID implements task.Handle.
func (h *Handle) ID() string {
	if h == nil {
		return ""
	}
	return h.taskID
}

// ChildSessionID returns the persistent child session associated with the task.
func (h *Handle) ChildSessionID() string {
	if h == nil {
		return ""
	}
	return h.childSessionID
}

// SendInput implements task.Handle.
func (h *Handle) SendInput(ctx context.Context, input *task.Input) error {
	if h == nil || h.sendInput == nil {
		return task.ErrMailboxNotFound
	}
	return h.sendInput(ctx, input)
}

// Wait implements task.Handle.
func (h *Handle) Wait(ctx context.Context) (*task.Outcome, error) {
	if h == nil || h.wait == nil {
		return nil, task.ErrMailboxNotFound
	}
	return h.wait(ctx)
}

// Cancel implements task.Handle.
func (h *Handle) Cancel(ctx context.Context, reason string) error {
	if h == nil || h.cancel == nil {
		return task.ErrMailboxNotFound
	}
	return h.cancel(ctx, reason)
}

var _ task.Handle = (*Handle)(nil)

// Result is one finite task outcome.
type Result[M adk.MessageType] struct {
	Handle       Handle
	FinalMessage M
	Interrupted  *adk.InterruptInfo
}

// StartOptions configures the new task created when Continue finds an idle
// child session.
type StartOptions[M adk.MessageType] struct {
	ParentSessionID string
	AgentName       string
	Description     string
	StartMode       task.StartMode
	OnEvent         func(*adk.TypedAgentEvent[M])
}

// ContinueRequest sends input to a persistent child session. IfIdle is required
// only when the session has no active task.
type ContinueRequest[M adk.MessageType] struct {
	ChildSessionID string
	InvocationID   string
	Input          *adk.TypedAgentInput[M]
	Delivery       task.InputDelivery
	IfIdle         *StartOptions[M]
}

// CompletionAction controls whether the finite task completes or suspends
// until Continue explicitly releases it.
type CompletionAction uint8

const (
	// CompletionUnknown is the invalid zero value.
	CompletionUnknown CompletionAction = iota
	CompletionComplete
	CompletionSuspend
)

// CompletionContext describes one completed child-agent turn.
type CompletionContext[M adk.MessageType] struct {
	TaskID         string
	ChildSessionID string
	AgentName      string
	FinalMessage   M
}

// CompletionBarrier decides whether the current finite task may complete.
type CompletionBarrier[M adk.MessageType] interface {
	Check(context.Context, *CompletionContext[M]) (CompletionAction, error)
}

// CancellationHook owns domain-specific cancellation cleanup. OnCancel may be
// retried by later cancellation requests and by recovery attempts, including
// on another worker or process, so implementations must be idempotent.
type CancellationHook interface {
	OnCancel(ctx context.Context, taskID, childSessionID, reason string) error
}

// InputPreemptPolicy maps a durable preempt intent to TurnLoop options. A nil
// result safely downgrades the input to queued delivery.
type InputPreemptPolicy[M adk.MessageType] func(
	context.Context,
	*task.InputRecord,
	*adk.TurnContext[*task.InputRecord, M],
) []adk.PushOption[*task.InputRecord, M]

type childSessionIDContextKey struct{}

func withChildSessionID(ctx context.Context, childSessionID string) context.Context {
	return context.WithValue(ctx, childSessionIDContextKey{}, childSessionID)
}

// ChildSessionID returns the current persistent child session ID.
func ChildSessionID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	value, ok := ctx.Value(childSessionIDContextKey{}).(string)
	return value, ok && value != ""
}
