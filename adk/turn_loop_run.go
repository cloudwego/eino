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

package adk

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/cloudwego/eino/internal/safe"
)

type turnRunSpec[T any, M MessageType] struct {
	runCtx             context.Context
	input              *TypedAgentInput[M]
	runOpts            []AgentRunOption
	resumeParams       *ResumeParams
	isResume           bool
	consumed           []T
	resumeCheckpointID string
	resumeBytes        []byte
}

type turnPlan[T any, M MessageType] struct {
	turnCtx   context.Context
	remaining []T
	spec      *turnRunSpec[T, M]
}

func (l *TurnLoop[T, M]) planTurn(
	ctx context.Context,
	isResume bool,
	items []T,
	pr *turnLoopPendingResume[T],
) (*turnPlan[T, M], error) {
	if !isResume {
		result, err := l.config.GenInput(ctx, l, items)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, errors.New("GenInputResult is nil")
		}
		if result.Input == nil {
			return nil, errors.New("agent input is nil")
		}
		turnCtx := ctx
		if result.RunCtx != nil {
			turnCtx = result.RunCtx
		}
		return &turnPlan[T, M]{
			turnCtx:   turnCtx,
			remaining: result.Remaining,
			spec: &turnRunSpec[T, M]{
				runCtx:   result.RunCtx,
				input:    result.Input,
				runOpts:  result.RunOpts,
				consumed: result.Consumed,
			},
		}, nil
	}
	if pr == nil {
		return nil, errors.New("resume payload is nil")
	}
	if l.config.GenResume == nil {
		return nil, errors.New("GenResume is required for resume")
	}
	resumeResult, err := l.config.GenResume(ctx, l, pr.interrupted, pr.unhandled, pr.resumeItems)
	if err != nil {
		return nil, err
	}
	if resumeResult == nil {
		return nil, errors.New("GenResumeResult is nil")
	}
	turnCtx := ctx
	if resumeResult.RunCtx != nil {
		turnCtx = resumeResult.RunCtx
	}
	switch resumeResult.Decision {
	case TurnLoopResumeDecisionResume:
		if resumeResult.Input != nil {
			return nil, errors.New("GenResumeResult.Input must be nil when resuming")
		}
		if len(pr.resumeBytes) == 0 {
			return nil, errors.New("resume checkpoint is empty")
		}
		resumeCheckpointID := pr.resumeCheckpointID
		if resumeCheckpointID == "" {
			resumeCheckpointID = bridgeCheckpointID
		}
		return &turnPlan[T, M]{
			turnCtx:   turnCtx,
			remaining: resumeResult.Remaining,
			spec: &turnRunSpec[T, M]{
				runCtx:             resumeResult.RunCtx,
				runOpts:            resumeResult.RunOpts,
				resumeParams:       resumeResult.ResumeParams,
				isResume:           true,
				consumed:           resumeResult.Consumed,
				resumeCheckpointID: resumeCheckpointID,
				resumeBytes:        pr.resumeBytes,
			},
		}, nil
	case TurnLoopResumeDecisionStartNewTurn:
		if resumeResult.Input == nil {
			return nil, errors.New("GenResumeResult.Input is nil for fresh turn")
		}
		return &turnPlan[T, M]{
			turnCtx:   turnCtx,
			remaining: resumeResult.Remaining,
			spec: &turnRunSpec[T, M]{
				runCtx:   resumeResult.RunCtx,
				input:    resumeResult.Input,
				runOpts:  resumeResult.RunOpts,
				consumed: resumeResult.Consumed,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown GenResume decision: %d", resumeResult.Decision)
	}
}

type turnExecution[T any, M MessageType] struct {
	consumed          []T
	capturedCancelErr *CancelError
	interruptContexts []*InterruptCtx
	checkpointBytes   []byte
	checkpointID      string
	interruptedItems  []T
}

type turnLoopNextItems[T any] struct {
	isResume bool
	pr       *turnLoopPendingResume[T]
	items    []T
	pushBack []T
}

func (l *TurnLoop[T, M]) collectNextTurnItems(ctx context.Context) (*turnLoopNextItems[T], bool) {
	next := &turnLoopNextItems[T]{}
	if l.pendingResume != nil {
		next.isResume = true
		var ok bool
		next.pr, ok = l.takePendingResume(ctx)
		if !ok {
			return nil, false
		}

		l.preemptCtrl.waitForPushes()
		buffered := l.buffer.TakeAll()
		if !next.pr.resumeSubmitted {
			next.pr.resumeItems = append(next.pr.resumeItems, buffered...)
		} else {
			next.pr.unhandled = append(next.pr.unhandled, buffered...)
		}

		next.pushBack = make([]T, 0, len(next.pr.interrupted)+len(next.pr.unhandled)+len(next.pr.resumeItems))
		next.pushBack = append(next.pushBack, next.pr.interrupted...)
		next.pushBack = append(next.pushBack, next.pr.unhandled...)
		next.pushBack = append(next.pushBack, next.pr.resumeItems...)
		return next, true
	}

	first, ok := l.receiveNextTurnItem(ctx)
	if !ok {
		return nil, false
	}

	if err := ctx.Err(); err != nil {
		l.buffer.PushFront([]T{first})
		l.runErr = err
		return nil, false
	}

	if l.stopCtrl.isCommitted() {
		l.buffer.PushFront([]T{first})
		return nil, false
	}

	l.preemptCtrl.waitForPushes()
	rest := l.buffer.TakeAll()
	next.items = append([]T{first}, rest...)
	next.pushBack = next.items
	return next, true
}

func (l *TurnLoop[T, M]) receiveNextTurnItem(ctx context.Context) (T, bool) {
	if idleFor := l.stopCtrl.idleDuration(); idleFor > 0 {
		return l.receiveNextTurnItemUntilIdle(ctx, idleFor)
	}
	first, ok := l.buffer.Receive()
	// Woken up by Stop(UntilIdleFor); re-enter loop to start the idle timer.
	if !ok && l.stopCtrl.idleDuration() > 0 {
		var zero T
		return zero, false
	}
	if !ok {
		if err := ctx.Err(); err != nil {
			l.runErr = err
		}
	}
	return first, ok
}

func (l *TurnLoop[T, M]) receiveNextTurnItemUntilIdle(ctx context.Context, idleFor time.Duration) (T, bool) {
	l.buffer.ClearWakeup()
	idleTimer := time.NewTimer(idleFor)
	cancelIdle := make(chan struct{})
	// When the idle timer fires, commitStop closes the buffer via buffer.Close(),
	// which broadcasts to unblock the pending Receive() call below.
	go func() {
		select {
		case <-idleTimer.C:
			l.commitStop()
		case <-cancelIdle:
		}
	}()

	first, ok := l.buffer.Receive()

	idleTimer.Stop()
	close(cancelIdle)

	if !ok {
		if err := ctx.Err(); err != nil {
			l.runErr = err
		}
		if !l.buffer.IsClosed() {
			var zero T
			return zero, false
		}
	}
	return first, ok
}

func (l *TurnLoop[T, M]) abortPlanningTurn() {
	l.preemptCtrl.abortPlanningTurn().ack()
}

func (l *TurnLoop[T, M]) pushBackAfterAbort(next *turnLoopNextItems[T]) {
	if len(next.pushBack) > 0 {
		l.buffer.PushFront(next.pushBack)
	}
}

func (l *TurnLoop[T, M]) planTurnOrAbort(ctx context.Context, next *turnLoopNextItems[T]) (*turnPlan[T, M], bool) {
	if next.isResume && l.stopCtrl.isCommitted() {
		l.restorePendingResume(next.pr)
		return nil, true
	}

	l.preemptCtrl.beginPlanningTurn()

	plan, err := l.planTurn(ctx, next.isResume, next.items, next.pr)
	if err != nil {
		l.abortPlanningTurn()
		l.pushBackAfterAbort(next)
		l.runErr = err
		return nil, true
	}

	if l.stopCtrl.isCommitted() {
		l.abortPlanningTurn()
		if next.isResume && plan.spec.isResume {
			l.restorePendingResume(next.pr)
			return nil, true
		}
		l.pushBackAfterAbort(next)
		return nil, true
	}

	return plan, false
}

func (l *TurnLoop[T, M]) prepareAgentOrAbort(
	_ context.Context,
	plan *turnPlan[T, M],
	next *turnLoopNextItems[T],
) (TypedAgent[M], bool) {
	agent, err := l.config.PrepareAgent(plan.turnCtx, l, plan.spec.consumed)
	if err != nil {
		l.abortPlanningTurn()
		l.pushBackAfterAbort(next)
		if next.isResume && !plan.spec.isResume {
			l.loadCheckpointID = ""
		}
		l.runErr = err
		return nil, true
	}

	if l.stopCtrl.isCommitted() {
		l.abortPlanningTurn()
		if next.isResume && plan.spec.isResume {
			l.restorePendingResume(next.pr)
			return nil, true
		}
		l.pushBackAfterAbort(next)
		return nil, true
	}

	return agent, false
}

func (l *TurnLoop[T, M]) run(ctx context.Context) {
	var lastExec *turnExecution[T, M]
	defer func() {
		l.cleanup(ctx, lastExec)
	}()

	if err := l.tryLoadCheckpoint(ctx); err != nil {
		l.runErr = err
		return
	}

	// Monitor context cancellation: close the buffer so that a blocking
	// Receive() unblocks. The loop will then check ctx.Err() and exit.
	go func() {
		select {
		case <-ctx.Done():
			l.buffer.Close()
		case <-l.done:
		}
	}()

	for {
		if l.stopCtrl.isCommitted() {
			return
		}

		next, ok := l.collectNextTurnItems(ctx)
		if !ok {
			if l.stopCtrl.idleDuration() > 0 && !l.stopCtrl.isCommitted() && !l.buffer.IsClosed() && ctx.Err() == nil {
				continue
			}
			return
		}

		plan, exit := l.planTurnOrAbort(ctx, next)
		if exit {
			return
		}

		agent, exit := l.prepareAgentOrAbort(ctx, plan, next)
		if exit {
			return
		}

		if next.isResume && !plan.spec.isResume && l.loadCheckpointID != "" {
			checkpointID := l.loadCheckpointID
			if err := l.deleteTurnLoopCheckpoint(ctx, checkpointID); err != nil {
				l.abortPlanningTurn()
				l.pushBackAfterAbort(next)
				l.loadCheckpointID = ""
				l.runErr = fmt.Errorf("failed to abandon checkpoint[%s] before fresh turn: %w", checkpointID, err)
				return
			}
			l.loadCheckpointID = ""
		}

		l.buffer.PushFront(plan.remaining)

		exec, runErr := l.runAgentAndHandleEvents(plan.turnCtx, agent, plan.spec)
		lastExec = exec
		runErr = l.deleteLoadedCheckpointAfterSuccessfulResume(ctx, runErr, plan.spec.isResume, exec.interruptContexts != nil)

		if runErr != nil {
			// Set interruptedItems when a cancel or interrupt was captured from the
			// event stream, regardless of what the user's callback returned. The items
			// were factually mid-execution when the signal arrived.
			if exec.capturedCancelErr != nil || exec.interruptContexts != nil {
				exec.interruptedItems = append([]T{}, plan.spec.consumed...)
			}
			l.runErr = runErr
			return
		}

		// Business interrupt: agent produced an Interrupted action, exit to persist checkpoint.
		if exec.interruptContexts != nil {
			exec.interruptedItems = append([]T{}, plan.spec.consumed...)
			l.runErr = &InterruptError{InterruptContexts: exec.interruptContexts}
			return
		}
	}
}

func (l *TurnLoop[T, M]) runAgentAndHandleEvents(
	ctx context.Context,
	agent TypedAgent[M],
	spec *turnRunSpec[T, M],
) (*turnExecution[T, M], error) {
	exec := &turnExecution[T, M]{
		consumed: append([]T{}, spec.consumed...),
	}
	var iter *AsyncIterator[*TypedAgentEvent[M]]

	runOpts, ms, err := l.setupBridgeStore(spec, spec.runOpts)
	if err != nil {
		l.preemptCtrl.abortPlanningTurn().ack()
		return exec, err
	}
	cancelOpt, agentCancelFunc := WithCancel()
	runOpts = append(runOpts, cancelOpt)

	// For Run path the streaming mode comes from the input. For Resume path the
	// runner reads the streaming mode persisted in the checkpoint, so the value we
	// pass here is irrelevant.
	enableStreaming := false
	if spec.input != nil {
		enableStreaming = spec.input.EnableStreaming
	}
	var runnerStore CheckPointStore
	if ms != nil {
		runnerStore = ms
	}
	runner := NewTypedRunner(TypedRunnerConfig[M]{
		EnableStreaming: enableStreaming,
		Agent:           agent,
		CheckPointStore: runnerStore,
		SessionID:       l.config.SessionID,
		SessionStore:    l.config.SessionStore,
		SessionConfig:   l.config.SessionConfig,
	})

	preemptDone := make(chan struct{})
	stoppedDone := make(chan struct{})

	tc := &TurnContext[T, M]{
		Loop:      l,
		Consumed:  spec.consumed,
		Preempted: preemptDone,
		Stopped:   stoppedDone,
		StopCause: l.stopCtrl.cause,
	}
	l.preemptCtrl.beginActiveTurn(ctx, tc)
	l.stopCtrl.beginActiveTurn()
	defer func() {
		l.stopCtrl.endActiveTurn()
		l.preemptCtrl.endActiveTurn().ack()
	}()

	if spec.isResume {
		var err error
		if spec.resumeParams != nil {
			iter, err = runner.ResumeWithParams(ctx, spec.resumeCheckpointID, spec.resumeParams, runOpts...)
		} else {
			iter, err = runner.Resume(ctx, spec.resumeCheckpointID, runOpts...)
		}
		if err != nil {
			return exec, fmt.Errorf("failed to resume agent: %w", err)
		}
	} else {
		iter = runner.Run(ctx, spec.input.Messages, runOpts...)
	}

	// Wrap iterator to capture framework-level signals (CancelError, InterruptContexts)
	// from events before they flow to OnAgentEvents. This ensures the framework can
	// track these signals independently of what the user's callback returns.
	srcIter := iter
	proxyIter, proxyGen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		defer proxyGen.Close()
		for {
			event, ok := srcIter.Next()
			if !ok {
				break
			}
			if event != nil {
				if event.Err != nil {
					var cancelErr *CancelError
					if errors.As(event.Err, &cancelErr) {
						exec.capturedCancelErr = cancelErr
					}
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					exec.interruptContexts = event.Action.Interrupted.InterruptContexts
				}
			}
			proxyGen.Send(event)
		}
	}()
	iter = proxyIter

	handleEvents := func() error {
		return l.onAgentEvents(ctx, tc, iter)
	}

	done := make(chan struct{})
	var handleErr error

	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				handleErr = safe.NewPanicErr(panicErr, debug.Stack())
			}
			close(done)
		}()
		handleErr = handleEvents()
	}()
	go l.watchPreempt(done, agentCancelFunc, preemptDone)
	go l.watchStop(done, agentCancelFunc, stoppedDone)

	finalizeCheckpoint := func() error {
		if ms != nil {
			key, data, ok := ms.LastCheckpoint()
			if ok {
				exec.checkpointID = key
				exec.checkpointBytes = append([]byte{}, data...)
			}
		}
		return nil
	}

	teardownTurn := func() {
		agentCancelFunc()
		for {
			if _, ok := iter.Next(); !ok {
				break
			}
		}
		<-proxyDone
	}

	// Wait for the turn to end. Three outcomes:
	//
	// done:         Events fully handled (normal or error). If Stop() was
	//               called, save checkpoint so the caller can resume later.
	//               Also handle the select race: if preemptDone is closed
	//               too, treat as a preempt (return nil) instead of leaking
	//               the CancelError.
	//
	// preemptDone:  A preemptive Push successfully cancelled the agent.
	//               Wait for the handleEvents goroutine to drain, then
	//               return nil — the run loop will start a new turn.
	//
	// stoppedDone:  Stop() cancelled the agent. Save checkpoint so the
	//               caller can resume later.
	select {
	case <-done:
		teardownTurn()
		select {
		case <-preemptDone:
			return exec, nil
		default:
		}
		if err := finalizeCheckpoint(); err != nil {
			if handleErr != nil {
				handleErr = fmt.Errorf("%w; checkpoint error: %v", handleErr, err)
			} else {
				handleErr = err
			}
		}
		return exec, applyFrameworkCapturedError(exec, handleErr)
	case <-preemptDone:
		<-done
		teardownTurn()
		return exec, nil
	case <-stoppedDone:
		<-done
		teardownTurn()
		if err := finalizeCheckpoint(); err != nil {
			if handleErr != nil {
				handleErr = fmt.Errorf("%w; checkpoint error: %v", handleErr, err)
			} else {
				handleErr = err
			}
		}
		return exec, applyFrameworkCapturedError(exec, handleErr)
	}
}

// applyFrameworkCapturedError resolves the final error for runAgentAndHandleEvents.
// Priority scheme:
//   - If handleErr != nil: the user's callback error wins (framework does not overwrite).
//   - If handleErr == nil and a CancelError was captured: use the captured CancelError.
//   - If handleErr == nil and interrupt contexts were captured: this is handled by the
//     caller (run loop) via l.interruptContexts, so return nil here.
//
// In all cases, the caller uses exec.capturedCancelErr and exec.interruptContexts to
// determine interruptedItems independently of the returned error.
func applyFrameworkCapturedError[T any, M MessageType](exec *turnExecution[T, M], handleErr error) error {
	if handleErr != nil {
		return handleErr
	}
	if exec.capturedCancelErr != nil {
		return exec.capturedCancelErr
	}
	return nil
}
