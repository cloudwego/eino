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
	"github.com/cloudwego/eino/adk/task/background"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
)

// DurableProgressReader projects a durable sub-agent's child session for
// task_output without exposing its session store.
type DurableProgressReader[M adk.MessageType] struct {
	controller *durablesubagent.Controller[M]
	format     TranscriptFormat[M]
}

// NewDurableProgressReader constructs a durable sub-agent progress reader
// from the Controller that owns the matching child-session authority.
func NewDurableProgressReader[M adk.MessageType](
	controller *durablesubagent.Controller[M],
	format TranscriptFormat[M],
) (*DurableProgressReader[M], error) {
	if controller == nil {
		return nil, errors.New("subagent: durable Controller is required to read task progress")
	}
	if format == nil {
		format = defaultTranscriptFormat[M]
	}
	return &DurableProgressReader[M]{controller: controller, format: format}, nil
}

// ReadProgress returns a bounded transcript for a matching durable sub-agent
// task and an empty string for tasks owned by other executors.
func (r *DurableProgressReader[M]) ReadProgress(
	ctx context.Context,
	task *background.TaskSnapshot,
) (string, error) {
	if r == nil || r.controller == nil {
		return "", errors.New("subagent: durable Controller is required to read task progress")
	}
	return r.controller.ReadProgress(
		ctx,
		task,
		r.format,
	)
}
