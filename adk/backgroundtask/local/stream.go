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
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/schema"
)

const streamBufferCap = 16

type streamChunk struct {
	text string
	err  error
}

type runResult struct {
	task *backgroundtask.Task
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
			if result.task != nil && result.task.Status == backgroundtask.StatusRunning {
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
	input    *Input
	spec     backgroundtask.Spec
	chunks   <-chan streamChunk
	resultCh <-chan struct {
		value string
		err   error
	}
	writer *schema.StreamWriter[string]
	cancel func()
}

func (r *Runner) projectForegroundStream(projection *foregroundStreamProjection) {
	ctx := projection.ctx
	input := projection.input
	spec := projection.spec
	chunks := projection.chunks
	resultCh := projection.resultCh
	writer := projection.writer
	cancel := projection.cancel
	defer writer.Close()
	startedAt := time.Now()
	timeoutMs := r.policy.TimeoutMs
	if input.ForegroundTimeoutMs != nil {
		timeoutMs = *input.ForegroundTimeoutMs
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if timeoutMs > 0 {
		timer = time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
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
				writer.Send("", chunk.err)
				return
			}
			if writer.Send(chunk.text, nil) {
				cancel()
				return
			}
		case result := <-resultCh:
			if !drainChunks(writer, chunks) {
				cancel()
				return
			}
			if result.err != nil {
				writer.Send("", result.err)
			}
			return
		case <-timeout:
			candidate := &backgroundtask.ForegroundCandidate{
				TaskID: spec.ID, Kind: spec.Kind, Description: spec.Description,
				OutputFile: spec.OutputFile, StartedAt: startedAt,
				Elapsed: time.Since(startedAt),
			}
			if r.policy.ShouldAutoBackground != nil &&
				r.policy.ShouldAutoBackground(ctx, candidate) {
				task, err := r.adoptForeground(ctx, spec, resultCh)
				if err != nil {
					cancel()
					writer.Send("", err)
					return
				}
				writer.Send(r.backgroundNotice(ctx, NoticeInfo{
					Task: task, AutoBackgrounded: true,
				}), nil)
				return
			}
			cancel()
			writer.Send("", &backgroundtask.ForegroundTimeoutError{
				Timeout: time.Duration(timeoutMs) * time.Millisecond,
				TaskID:  spec.ID,
			})
			return
		case <-ctx.Done():
			cancel()
			return
		}
		if chunks == nil {
			result := <-resultCh
			if result.err != nil {
				writer.Send("", result.err)
			}
			return
		}
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
