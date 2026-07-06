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
	"time"
)

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
