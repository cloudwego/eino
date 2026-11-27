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

func takeRunnerCheckpoint[T any](cp *turnLoopCheckpoint[T]) []byte {
	runnerCheckpoint := cp.RunnerCheckpoint
	cp.RunnerCheckpoint = nil
	return runnerCheckpoint
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

// Stores without Delete can retire a loaded checkpoint by replacing it with
// an empty between-turns checkpoint. Subsequent loops treat it as a fresh start.
func (l *TurnLoop[T, M]) retireLoadedCheckpoint(ctx context.Context) error {
	if deleter, ok := l.config.Store.(CheckPointDeleter); ok {
		return deleter.Delete(ctx, l.loadCheckpointID)
	}
	return l.saveTurnLoopCheckpoint(ctx, l.loadCheckpointID, &turnLoopCheckpoint[T]{})
}

func (l *TurnLoop[T, M]) retireCheckpointAfterSuccessfulResume(ctx context.Context, runErr error, exec *turnExecution[T, M]) error {
	if runErr != nil || !exec.resumeConsumed || exec.interruptContexts != nil || l.loadCheckpointID == "" {
		return runErr
	}
	checkpointID := l.loadCheckpointID
	if err := l.retireLoadedCheckpoint(ctx); err != nil {
		return fmt.Errorf("failed to retire consumed checkpoint[%s] after resume: %w", checkpointID, err)
	}
	l.loadCheckpointID = ""
	return nil
}

func (l *TurnLoop[T, M]) tryLoadCheckpoint(ctx context.Context) error {
	checkPointID := l.config.CheckpointID
	if checkPointID == "" || l.config.Store == nil {
		return nil
	}

	data, existed, err := l.config.Store.Get(ctx, checkPointID)
	if err != nil {
		return fmt.Errorf("failed to load checkpoint[%s]: %w", checkPointID, err)
	}
	if !existed {
		return nil
	}

	if len(data) == 0 {
		return nil
	}
	cp, err := unmarshalTurnLoopCheckpoint[T](data)
	if err != nil {
		return fmt.Errorf("failed to unmarshal checkpoint[%s]: %w", checkPointID, err)
	}

	if !cp.HasRunnerState && len(cp.UnhandledItems) == 0 {
		return nil
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
		l.pendingResume = &turnLoopPendingResume[T]{
			interrupted:        cp.CanceledItems,
			unhandled:          cp.UnhandledItems,
			resumeItems:        cp.ResumeItems,
			resumeSubmitted:    len(cp.ResumeItems) > 0,
			resumeCheckpointID: resumeCheckpointID,
			resumeBytes:        takeRunnerCheckpoint(cp),
		}
		l.pendingResume.addBufferedItems(newItems)
	} else {
		items := make([]T, 0, len(cp.UnhandledItems)+len(newItems))
		items = append(items, cp.UnhandledItems...)
		items = append(items, newItems...)
		l.buffer.PushFront(items)
	}
	l.loadCheckpointID = checkPointID

	return nil
}

type turnLoopPendingResume[T any] struct {
	interrupted        []T
	unhandled          []T
	resumeItems        []T
	resumeSubmitted    bool
	itemsAdded         bool
	resumeCheckpointID string
	resumeBytes        []byte
}

func (pr *turnLoopPendingResume[T]) addBufferedItems(items []T) {
	if len(items) == 0 {
		return
	}
	pr.itemsAdded = true
	if pr.resumeSubmitted {
		pr.unhandled = append(pr.unhandled, items...)
	} else {
		pr.resumeItems = append(pr.resumeItems, items...)
	}
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

	l.buffer.Close()
	unhandled := l.buffer.TakeAll()
	pending := l.pendingResume
	pendingChanged := pending != nil && (pending.itemsAdded || len(unhandled) > 0)
	if pending != nil {
		unhandled = append(append([]T{}, pending.unhandled...), unhandled...)
	}
	checkpointID := l.config.CheckpointID
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
	isIdle := len(checkpointBytes) == 0 && pending == nil && len(unhandled) == 0 && len(interruptedItems) == 0

	exitCausedByStop := l.runErr == nil || errors.As(l.runErr, new(*CancelError)) || capturedCancelErr != nil
	businessInterrupt := errors.As(l.runErr, new(*InterruptError)) || interruptContexts != nil
	// A failed turn can still leave a new runner checkpoint or unprocessed
	// items from the loaded checkpoint. Preserve them for the next loop.
	saveRemaining := l.loadCheckpointID != "" && pending == nil && l.runErr != nil &&
		(len(unhandled) > 0 || len(checkpointBytes) > 0)
	shouldSaveCheckpoint := l.config.Store != nil && checkpointID != "" &&
		((l.stopCtrl.isCommitted() && exitCausedByStop) || businessInterrupt || pendingChanged || saveRemaining) &&
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
			resumeItems = pending.resumeItems
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
	} else if l.loadCheckpointID != "" && (pending == nil || l.stopCtrl.skipCheckpointEnabled()) {
		checkpointErr = l.retireLoadedCheckpoint(ctx)
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
	close(l.done)
}
