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

// Package worker provides a minimal polling dispatcher for durable background tasks.
package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

// WorkerConfig configures a polling Worker.
type WorkerConfig struct {
	Manager            *backgroundtask.Manager
	ExecutorKeys       []string
	PollInterval       time.Duration
	InitialPickupDelay time.Duration
	MaxConcurrent      int
}

// Worker polls pending tasks and dispatches claims through Manager.Execute.
// TaskStore authorization remains authoritative when multiple Workers race.
type Worker struct {
	manager            *backgroundtask.Manager
	executorKeys       []string
	pollInterval       time.Duration
	initialPickupDelay time.Duration
	maxConcurrent      int
}

// NewWorker creates a polling Worker.
func NewWorker(config WorkerConfig) (*Worker, error) {
	if config.Manager == nil {
		return nil, errors.New("backgroundtask/worker: manager is required")
	}
	if len(config.ExecutorKeys) == 0 {
		return nil, errors.New("backgroundtask/worker: executor keys are required")
	}
	keys := make([]string, len(config.ExecutorKeys))
	for i, key := range config.ExecutorKeys {
		if key == "" {
			return nil, errors.New("backgroundtask/worker: executor key is required")
		}
		keys[i] = key
	}
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.InitialPickupDelay < 0 {
		return nil, errors.New("backgroundtask/worker: initial pickup delay cannot be negative")
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 1
	}
	return &Worker{
		manager: config.Manager, executorKeys: keys,
		pollInterval: config.PollInterval, initialPickupDelay: config.InitialPickupDelay,
		maxConcurrent: config.MaxConcurrent,
	}, nil
}

// Run polls until ctx is canceled, then stops claiming and waits for dispatch
// calls that observe the same context to return.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil || w.manager == nil {
		return errors.New("backgroundtask/worker: worker is required")
	}
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	semaphore := make(chan struct{}, w.maxConcurrent)
	dispatchErrors := make(chan error, w.maxConcurrent)
	var attempts sync.WaitGroup
	inFlight := make(map[string]struct{})
	var inFlightMu sync.Mutex

	dispatch := func(task *backgroundtask.Task) {
		if task == nil {
			return
		}
		if task.Attempt == 0 && time.Now().Before(task.Spec.CreatedAt.Add(w.initialPickupDelay)) {
			return
		}
		taskID := task.Spec.ID
		inFlightMu.Lock()
		if _, exists := inFlight[taskID]; exists {
			inFlightMu.Unlock()
			return
		}
		inFlight[taskID] = struct{}{}
		inFlightMu.Unlock()
		select {
		case semaphore <- struct{}{}:
		case <-runCtx.Done():
			inFlightMu.Lock()
			delete(inFlight, taskID)
			inFlightMu.Unlock()
			return
		}
		attempts.Add(1)
		go func() {
			defer attempts.Done()
			defer func() {
				<-semaphore
				inFlightMu.Lock()
				delete(inFlight, taskID)
				inFlightMu.Unlock()
			}()
			if err := w.manager.Execute(runCtx, taskID); err != nil &&
				!benignDispatchError(runCtx, err) {
				select {
				case dispatchErrors <- err:
				case <-runCtx.Done():
				}
			}
		}()
	}

	poll := func() error {
		cursor := ""
		for {
			result, err := w.manager.ListPending(ctx, &backgroundtask.ListPendingRequest{
				ExecutorKeys: w.executorKeys, Cursor: cursor, Limit: 100,
			})
			if err != nil {
				return err
			}
			for _, task := range result.Tasks {
				if runCtx.Err() != nil {
					return runCtx.Err()
				}
				dispatch(task)
			}
			if result.NextCursor == "" {
				return nil
			}
			cursor = result.NextCursor
		}
	}

	for {
		if err := poll(); err != nil {
			if runCtx.Err() != nil {
				attempts.Wait()
				return nil
			}
			attempts.Wait()
			return err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			cancel()
			attempts.Wait()
			return nil
		case err := <-dispatchErrors:
			cancel()
			attempts.Wait()
			return err
		}
	}
}

func benignDispatchError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return true
	}
	if errors.Is(err, backgroundtask.ErrVersionConflict) ||
		errors.Is(err, backgroundtask.ErrIllegalTransition) ||
		errors.Is(err, backgroundtask.ErrAlreadyTerminal) ||
		errors.Is(err, backgroundtask.ErrNotFound) {
		return true
	}
	return strings.Contains(err.Error(), "task is already executing in this manager")
}
