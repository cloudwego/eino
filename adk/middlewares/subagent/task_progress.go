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
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
)

// DurableTaskProgressReader projects a durable sub-agent's child session for
// task_output without exposing its session store.
type DurableTaskProgressReader[M adk.MessageType] struct {
	executor *durablesubagent.Executor[M]
	format   TranscriptFormat[M]
}

// NewDurableTaskProgressReader constructs a durable sub-agent progress reader
// from the executor that owns the matching child-session authority.
func NewDurableTaskProgressReader[M adk.MessageType](
	executor *durablesubagent.Executor[M],
	format TranscriptFormat[M],
) (*DurableTaskProgressReader[M], error) {
	if executor == nil {
		return nil, errors.New("subagent: durable executor is required to read task progress")
	}
	if format == nil {
		format = defaultTranscriptFormat[M]
	}
	return &DurableTaskProgressReader[M]{executor: executor, format: format}, nil
}

// ReadProgress returns a bounded transcript for a matching durable sub-agent
// task and an empty string for tasks owned by other executors.
func (r *DurableTaskProgressReader[M]) ReadProgress(
	ctx context.Context,
	task *backgroundtask.Task,
) (string, error) {
	if r == nil || r.executor == nil {
		return "", errors.New("subagent: durable executor is required to read task progress")
	}
	return r.executor.ReadProgress(
		ctx,
		task,
		r.format,
	)
}
