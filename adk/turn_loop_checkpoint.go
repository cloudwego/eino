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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

type turnLoopCheckpoint[T any] struct {
	RunnerCheckpointID string
	RunnerCheckpoint   []byte
	// HasRunnerState reports whether RunnerCheckpoint contains resumable runner state.
	// It is false for "between turns" checkpoints where no agent execution was
	// interrupted (e.g. Stop() before the first turn or between turns).
	HasRunnerState bool
	UnhandledItems []T
	ResumeItems    []T
	CanceledItems  []T // gob-compat: kept as CanceledItems for deserialization of existing checkpoints
}

func marshalTurnLoopCheckpoint[T any](c *turnLoopCheckpoint[T]) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unmarshalTurnLoopCheckpoint[T any](data []byte) (*turnLoopCheckpoint[T], error) {
	var c turnLoopCheckpoint[T]
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (l *TurnLoop[T, M]) saveTurnLoopCheckpoint(ctx context.Context, checkPointID string, c *turnLoopCheckpoint[T]) error {
	if l.config.Store == nil {
		return errors.New("checkpoint store is nil")
	}
	data, err := marshalTurnLoopCheckpoint(c)
	if err != nil {
		return err
	}
	return l.config.Store.Set(ctx, checkPointID, data)
}

func (l *TurnLoop[T, M]) deleteTurnLoopCheckpoint(ctx context.Context, checkPointID string) error {
	if l.config.Store == nil {
		return nil
	}
	if deleter, ok := l.config.Store.(CheckPointDeleter); ok {
		return deleter.Delete(ctx, checkPointID)
	}
	return nil
}

func (l *TurnLoop[T, M]) deleteLoadedCheckpointAfterSuccessfulResume(ctx context.Context, runErr error, isResume bool, hasInterrupt bool) error {
	if runErr != nil || !isResume || hasInterrupt || l.loadCheckpointID == "" {
		return runErr
	}
	checkpointID := l.loadCheckpointID
	if err := l.deleteTurnLoopCheckpoint(ctx, checkpointID); err != nil {
		return fmt.Errorf("failed to delete consumed checkpoint[%s] after resume: %w", checkpointID, err)
	}
	l.loadCheckpointID = ""
	return nil
}

func (l *TurnLoop[T, M]) tryLoadCheckpoint(ctx context.Context) error {
	checkPointID := l.config.CheckpointID
	if checkPointID == "" || l.config.Store == nil {
		return nil
	}

	l.loadCheckpointID = checkPointID

	data, existed, err := l.config.Store.Get(ctx, checkPointID)
	if err != nil {
		return fmt.Errorf("failed to load checkpoint[%s]: %w", checkPointID, err)
	}
	if !existed {
		return nil
	}

	var cp *turnLoopCheckpoint[T]
	if len(data) == 0 {
		return nil
	}
	cp, err = unmarshalTurnLoopCheckpoint[T](data)
	if err != nil {
		return fmt.Errorf("failed to unmarshal checkpoint[%s]: %w", checkPointID, err)
	}

	newItems := l.buffer.TakeAll()

	if cp.HasRunnerState {
		if len(cp.RunnerCheckpoint) == 0 {
			l.buffer.PushFront(newItems)
			return fmt.Errorf("checkpoint[%s] has runner state but bytes are empty", checkPointID)
		}
		resumeCheckpointID := cp.RunnerCheckpointID
		if resumeCheckpointID == "" {
			resumeCheckpointID = bridgeCheckpointID
		}
		resumeItems := append([]T{}, cp.ResumeItems...)
		resumeSubmitted := len(resumeItems) > 0
		if !resumeSubmitted {
			resumeItems = append(resumeItems, newItems...)
		} else {
			unhandled := make([]T, 0, len(cp.UnhandledItems)+len(newItems))
			unhandled = append(unhandled, cp.UnhandledItems...)
			unhandled = append(unhandled, newItems...)
			cp.UnhandledItems = unhandled
		}
		l.pendingResume = &turnLoopPendingResume[T]{
			interrupted:        append([]T{}, cp.CanceledItems...),
			unhandled:          append([]T{}, cp.UnhandledItems...),
			resumeItems:        resumeItems,
			resumeSubmitted:    resumeSubmitted,
			resumeCheckpointID: resumeCheckpointID,
			resumeBytes:        append([]byte{}, cp.RunnerCheckpoint...),
		}
	} else {
		items := make([]T, 0, len(cp.UnhandledItems)+len(newItems))
		items = append(items, cp.UnhandledItems...)
		items = append(items, newItems...)
		l.buffer.PushFront(items)
	}

	return nil
}

type turnLoopPendingResume[T any] struct {
	interrupted        []T
	unhandled          []T
	resumeItems        []T
	resumeSubmitted    bool
	resumeCheckpointID string
	resumeBytes        []byte
}

// SafePoint describes at which boundary the agent may be cancelled.
// It is a bitmask: values can be combined with bitwise OR to accept multiple
// safe points (e.g. AfterToolCalls | AfterChatModel). Internally, SafePoint
// is translated to CancelMode via toCancelMode().
//
// SafePoint is used only in the preemption API (WithPreempt/WithPreemptTimeout).
// A key design constraint: preemption always targets a safe point — the user's
// intent is to cancel at a well-defined boundary, never to abort immediately.
// Immediate cancellation is only reachable as an automatic timeout escalation
// (via WithPreemptTimeout), not as a direct user choice. This is why SafePoint
// has no "immediate" value and why WithPreempt requires a non-zero SafePoint
// (panics otherwise).
func (l *TurnLoop[T, M]) takePendingResume(_ context.Context) (*turnLoopPendingResume[T], bool) {
	l.resumeMu.Lock()
	pr := l.pendingResume
	if pr == nil {
		l.resumeMu.Unlock()
		return nil, false
	}
	l.pendingResume = nil
	l.resumeMu.Unlock()
	return pr, true
}

func (l *TurnLoop[T, M]) restorePendingResume(pr *turnLoopPendingResume[T]) {
	if pr == nil {
		return
	}
	l.resumeMu.Lock()
	defer l.resumeMu.Unlock()
	l.pendingResume = pr
}

func (l *TurnLoop[T, M]) setupBridgeStore(spec *turnRunSpec[T, M], runOpts []AgentRunOption) ([]AgentRunOption, *bridgeStore, error) {
	needsBridge := l.config.Store != nil || spec.isResume
	if !needsBridge {
		return runOpts, nil, nil
	}
	checkpointID := bridgeCheckpointID
	if spec.resumeCheckpointID != "" {
		checkpointID = spec.resumeCheckpointID
	}
	runOpts = append(runOpts, WithCheckPointID(checkpointID))
	if spec.isResume {
		if len(spec.resumeBytes) == 0 {
			return nil, nil, fmt.Errorf("resume checkpoint is empty")
		}
		return runOpts, newResumeBridgeStore(checkpointID, spec.resumeBytes), nil
	}
	return runOpts, newBridgeStore(), nil
}

func interruptedItemsForExit[T any](items []T, pending *turnLoopPendingResume[T]) []T {
	if pending != nil {
		return pending.interrupted
	}
	return items
}

func (l *TurnLoop[T, M]) cleanup(ctx context.Context, exec *turnExecution[T, M]) {
	atomic.StoreInt32(&l.stopped, 1)

	unhandled := l.buffer.TakeAll()
	l.resumeMu.Lock()
	pending := l.pendingResume
	l.resumeMu.Unlock()
	if pending != nil {
		unhandled = append(append([]T{}, pending.unhandled...), unhandled...)
	}
	checkpointID := l.config.CheckpointID
	hasPendingRunnerState := pending != nil && len(pending.resumeBytes) > 0
	var checkpointBytes []byte
	var runnerCheckpointID string
	var interruptedItems []T
	var capturedCancelErr *CancelError
	var interruptContexts []*InterruptCtx
	if exec != nil {
		checkpointBytes = exec.checkpointBytes
		runnerCheckpointID = exec.checkpointID
		interruptedItems = exec.interruptedItems
		capturedCancelErr = exec.capturedCancelErr
		interruptContexts = exec.interruptContexts
	}
	isIdle := len(checkpointBytes) == 0 && !hasPendingRunnerState && len(unhandled) == 0 && len(interruptedItems) == 0

	// Only save checkpoint when the loop exited due to an explicit Stop(),
	// a CancelError, or a business interrupt (InterruptError).
	// Also checkpoint when a cancel/interrupt was captured from the event stream
	// but the user's callback returned a custom error (the items were still in-flight).
	exitCausedByStop := l.runErr == nil || errors.As(l.runErr, new(*CancelError)) || capturedCancelErr != nil
	businessInterrupt := errors.As(l.runErr, new(*InterruptError)) || interruptContexts != nil
	shouldSaveCheckpoint := l.config.Store != nil && checkpointID != "" &&
		((l.stopCtrl.isCommitted() && exitCausedByStop) || businessInterrupt || (pending != nil && hasPendingRunnerState)) &&
		!isIdle && !l.stopCtrl.skipCheckpointEnabled()

	var checkpointAttempted bool
	var checkpointErr error

	if shouldSaveCheckpoint {
		runnerCheckpoint := checkpointBytes
		var resumeItems []T
		if pending != nil {
			runnerCheckpointID = pending.resumeCheckpointID
			runnerCheckpoint = pending.resumeBytes
			interruptedItems = pending.interrupted
			if pending.resumeSubmitted {
				resumeItems = append([]T{}, pending.resumeItems...)
			}
		}
		cp := &turnLoopCheckpoint[T]{
			RunnerCheckpointID: runnerCheckpointID,
			RunnerCheckpoint:   runnerCheckpoint,
			HasRunnerState:     len(runnerCheckpoint) > 0,
			UnhandledItems:     unhandled,
			ResumeItems:        resumeItems,
			CanceledItems:      interruptedItems,
		}
		checkpointAttempted = true
		checkpointErr = l.saveTurnLoopCheckpoint(ctx, checkpointID, cp)
	} else if l.loadCheckpointID != "" {
		_ = l.deleteTurnLoopCheckpoint(ctx, l.loadCheckpointID)
	}

	var takeLateOnce sync.Once
	var takeLateResult []T

	l.result = &TurnLoopExitState[T, M]{
		ExitReason:          l.runErr,
		UnhandledItems:      unhandled,
		InterruptedItems:    interruptedItemsForExit(interruptedItems, pending),
		StopCause:           l.stopCtrl.cause(),
		CheckpointAttempted: checkpointAttempted,
		CheckpointErr:       checkpointErr,
		TakeLateItems: func() []T {
			takeLateOnce.Do(func() {
				l.lateMu.Lock()
				takeLateResult = append([]T{}, l.lateItems...)
				l.lateSealed = true
				l.lateMu.Unlock()
			})
			return takeLateResult
		},
	}

	l.stopCtrl.closeForLoopExit()
	l.preemptCtrl.closeForLoopExit()
	l.buffer.Close()
	close(l.done)
}
