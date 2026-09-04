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

package local

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk/internal/taskfirst"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

const streamBufferCap = 16

type streamChunk struct {
	text string
	err  error
}

type runResult struct {
	task *background.TaskSnapshot
	err  error
}

func (r *Runner) projectStream(
	ctx context.Context,
	input *Input,
	taskID string,
	chunks <-chan streamChunk,
	runDone <-chan runResult,
	writer *schema.StreamWriter[string],
) {
	var preview <-chan time.Time
	if input.RunInBackground && input.BackgroundStartupPreviewMs > 0 {
		timer := time.NewTimer(time.Duration(input.BackgroundStartupPreviewMs) * time.Millisecond)
		defer timer.Stop()
		preview = timer.C
	}
	forward := !input.RunInBackground || preview != nil
	callerOpen := true
	callerDone := ctx.Done()
	if input.RunInBackground && preview == nil {
		r.sendNotice(ctx, writer, taskID, false)
		writer.Close()
		callerOpen = false
	}
	for chunks != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if chunk.err != nil {
				if callerOpen {
					writer.Send("", chunk.err)
					writer.Close()
					callerOpen = false
				}
				forward = false
				continue
			}
			if forward && callerOpen && writer.Send(chunk.text, nil) {
				callerOpen = false
				forward = false
				if !input.RunInBackground {
					_, _ = r.manager.RequestCancel(context.Background(), taskID)
				}
			}
		case result := <-runDone:
			if result.err != nil {
				if callerOpen {
					writer.Send("", result.err)
					writer.Close()
					callerOpen = false
				}
				forward = false
				continue
			}
			if result.task != nil && result.task.Status == background.StatusRunning {
				if !input.RunInBackground {
					if callerOpen {
						r.sendNotice(ctx, writer, taskID, true)
						writer.Close()
						callerOpen = false
					}
					forward = false
				}
				continue
			}
			if input.RunInBackground && callerOpen {
				r.sendNotice(ctx, writer, taskID, false)
				writer.Close()
				callerOpen = false
				forward = false
			}
		case <-preview:
			preview = nil
			forward = false
			if callerOpen {
				r.sendNotice(ctx, writer, taskID, false)
				writer.Close()
				callerOpen = false
			}
		case <-callerDone:
			callerDone = nil
			if !input.RunInBackground {
				_, _ = r.manager.RequestCancel(context.Background(), taskID)
			}
			if callerOpen {
				writer.Close()
				callerOpen = false
			}
			forward = false
		}
	}
	if callerOpen {
		if input.RunInBackground {
			r.sendNotice(ctx, writer, taskID, false)
		}
		writer.Close()
	}
}

type foregroundStreamProjection struct {
	ctx      context.Context
	chunks   <-chan streamChunk
	resultCh <-chan struct {
		value string
		err   error
	}
	writer    *schema.StreamWriter[string]
	cancel    func()
	finalizer *taskfirst.ForegroundMailboxFinalizer
	taskID    string
	timeoutMs int
}

func (r *Runner) awaitTaskFirstStreamConstruction(
	ctx context.Context,
	input *Input,
	execution *taskfirst.Execution,
	chunks <-chan streamChunk,
	ready <-chan error,
) (*schema.StreamReader[string], error) {
	startProjection := func(readyErr error) (*schema.StreamReader[string], error) {
		if readyErr != nil {
			boundary, waitErr := execution.WaitBoundary(context.Background())
			if waitErr != nil {
				return nil, waitErr
			}
			if boundary != nil && boundary.ResultError != "" {
				return nil, errors.New(boundary.ResultError)
			}
			return nil, readyErr
		}
		reader, writer := schema.Pipe[string](streamBufferCap)
		go r.projectTaskFirstStream(ctx, input, execution, chunks, writer)
		return reader, nil
	}
	resolvedStream := func(resolve func(*schema.StreamWriter[string])) *schema.StreamReader[string] {
		reader, writer := schema.Pipe[string](streamBufferCap)
		resolve(writer)
		writer.Close()
		return reader
	}

	select {
	case readyErr := <-ready:
		return startProjection(readyErr)
	case <-execution.Timeout():
		return resolvedStream(func(writer *schema.StreamWriter[string]) {
			r.resolveTaskFirstStreamTimeout(ctx, execution, writer)
		}), nil
	case <-ctx.Done():
		return resolvedStream(func(writer *schema.StreamWriter[string]) {
			r.resolveTaskFirstStreamCallerAbort(ctx, execution, writer)
		}), nil
	case <-execution.Boundary():
		// Constructor failures signal ready before the attempt commits its
		// boundary. Preserve that specific error when both are observable.
		select {
		case readyErr := <-ready:
			return startProjection(readyErr)
		default:
		}
		boundary, err := execution.WaitBoundary(context.Background())
		if err != nil {
			return nil, err
		}
		if boundary != nil && boundary.ResultError != "" {
			return nil, errors.New(boundary.ResultError)
		}
		return nil, errors.New("task/local: task reached a boundary before stream construction")
	}
}

func (r *Runner) projectForegroundStream(projection *foregroundStreamProjection) {
	ctx := projection.ctx
	chunks := projection.chunks
	resultCh := projection.resultCh
	writer := projection.writer
	cancel := projection.cancel
	defer writer.Close()
	var timeout <-chan time.Time
	var timer *time.Timer
	if projection.timeoutMs > 0 {
		timer = time.NewTimer(time.Duration(projection.timeoutMs) * time.Millisecond)
		timeout = timer.C
		defer timer.Stop()
	}
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if chunk.err != nil {
				writer.Send(
					"",
					taskfirst.CombineForegroundErrors(
						chunk.err,
						projection.finalizer.Abandon(),
					),
				)
				return
			}
			if writer.Send(chunk.text, nil) {
				cancel()
				_ = projection.finalizer.Abandon()
				return
			}
		case result := <-resultCh:
			callerClosed, drainErr := drainForegroundChunks(writer, chunks)
			if callerClosed {
				cancel()
				_ = projection.finalizer.Abandon()
				return
			}
			if drainErr != nil {
				cancel()
				writer.Send(
					"",
					taskfirst.CombineForegroundErrors(
						drainErr,
						projection.finalizer.Abandon(),
					),
				)
				return
			}
			if result.err != nil {
				writer.Send(
					"",
					taskfirst.CombineForegroundErrors(
						result.err,
						projection.finalizer.Abandon(),
					),
				)
			} else {
				if err := projection.finalizer.SealIfIdle(); err != nil {
					writer.Send(
						"",
						fmt.Errorf("task/local: seal foreground mailbox: %w", err),
					)
				}
			}
			return
		case <-timeout:
			cancel()
			writer.Send(
				"",
				taskfirst.CombineForegroundErrors(
					&task.ForegroundTimeoutError{
						Timeout: time.Duration(projection.timeoutMs) * time.Millisecond,
						TaskID:  projection.taskID,
					},
					projection.finalizer.Abandon(),
				),
			)
			return
		case <-ctx.Done():
			cancel()
			if err := projection.finalizer.Abandon(); err != nil {
				writer.Send(
					"",
					taskfirst.CombineForegroundErrors(ctx.Err(), err),
				)
			}
			return
		}
	}
}

func drainForegroundChunks(
	writer *schema.StreamWriter[string],
	chunks <-chan streamChunk,
) (callerClosed bool, err error) {
	if chunks == nil {
		return false, nil
	}
	for chunk := range chunks {
		if chunk.err != nil {
			return false, chunk.err
		}
		if writer.Send(chunk.text, nil) {
			return true, nil
		}
	}
	return false, nil
}

func (r *Runner) projectTaskFirstStream(
	ctx context.Context,
	input *Input,
	execution *taskfirst.Execution,
	chunks <-chan streamChunk,
	writer *schema.StreamWriter[string],
) {
	defer writer.Close()
	boundary := execution.Boundary()
	timeout := execution.Timeout()
	for {
		select {
		case chunk, open := <-chunks:
			if !open {
				chunks = nil
				continue
			}
			if chunk.err != nil {
				writer.Send("", chunk.err)
				return
			}
			if writer.Send(chunk.text, nil) {
				_, _ = execution.ResolveCallerAbort(
					ctx,
					context.Canceled,
				)
				return
			}
		case <-boundary:
			if !drainChunks(writer, chunks) {
				return
			}
			task, err := execution.WaitBoundary(context.Background())
			if err != nil {
				writer.Send("", err)
				return
			}
			if task != nil && task.ResultError != "" {
				writer.Send("", errors.New(task.ResultError))
			}
			return
		case <-timeout:
			r.resolveTaskFirstStreamTimeout(ctx, execution, writer)
			return
		case <-ctx.Done():
			r.resolveTaskFirstStreamCallerAbort(ctx, execution, writer)
			return
		}
	}
}

func (r *Runner) resolveTaskFirstStreamTimeout(
	ctx context.Context,
	execution *taskfirst.Execution,
	writer *schema.StreamWriter[string],
) {
	outcome, err := execution.ResolveTimeout(ctx)
	if err != nil {
		writer.Send("", err)
		return
	}
	if outcome != nil && outcome.Backgrounded {
		r.sendNotice(ctx, writer, execution.TaskID(), true)
		return
	}
	writer.Send("", execution.ForegroundTimeoutError())
}

func (r *Runner) resolveTaskFirstStreamCallerAbort(
	ctx context.Context,
	execution *taskfirst.Execution,
	writer *schema.StreamWriter[string],
) {
	_, err := execution.ResolveCallerAbort(ctx, ctx.Err())
	if err != nil {
		writer.Send("", err)
	}
}

func drainChunks(writer *schema.StreamWriter[string], chunks <-chan streamChunk) bool {
	if chunks == nil {
		return true
	}
	for chunk := range chunks {
		if chunk.err != nil {
			writer.Send("", chunk.err)
			return false
		}
		if writer.Send(chunk.text, nil) {
			return false
		}
	}
	return true
}

func (r *Runner) sendNotice(
	ctx context.Context,
	writer *schema.StreamWriter[string],
	taskID string,
	autoBackgrounded bool,
) {
	task, _ := r.manager.Get(context.Background(), taskID)
	writer.Send(r.backgroundNotice(ctx, NoticeInfo{
		Task: task, AutoBackgrounded: autoBackgrounded,
	}), nil)
}
