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
		writer.Close()
	}
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
