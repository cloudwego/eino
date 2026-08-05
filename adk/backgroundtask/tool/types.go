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

// Package tool adapts explicitly capable external tools to durable background tasks.
package tool

import (
	"context"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/schema"
)

const (
	// ExecutorKey identifies non-recoverable managed tools.
	ExecutorKey = "eino.dev/background-tool"
	// RecoverableExecutorKey identifies managed tools that support Worker handoff.
	RecoverableExecutorKey = "eino.dev/recoverable-background-tool"
)

// BackgroundTool starts one logical external operation. Start receives the Eino
// task ID before any external side effect occurs.
type BackgroundTool interface {
	ValidateArguments(arguments string) error
	Start(context.Context, *StartRequest) (Run, error)
}

// RecoverableBackgroundTool reconstructs the same logical operation after a
// Worker loss or graceful yield. Implementations must make Start idempotent by
// TaskID and keep recovery state durable for the task lifetime.
type RecoverableBackgroundTool interface {
	BackgroundTool
	Recover(context.Context, *RecoverRequest) (Run, error)
}

// StartRequest describes an initial external-operation start.
type StartRequest struct {
	TaskID    string
	Arguments string
	Attempt   int64
}

// RecoverRequest describes reconstruction of an existing logical operation.
type RecoverRequest struct {
	TaskID    string
	Arguments string
	Attempt   int64
}

// Run is an attempt-local handle for one logical external operation. Canceling
// the context passed to Wait stops observation only; Stop explicitly requests
// logical-operation cancellation.
type Run interface {
	Wait(context.Context) (*Outcome, error)
	Stop(context.Context) error
}

// UpdateSource optionally exposes replayable incremental updates.
type UpdateSource interface {
	Updates() *schema.StreamReader[*Update]
}

// Outcome is the authoritative logical-operation terminal result.
type Outcome struct {
	Status backgroundtask.Status
	Data   []byte
	Error  string
}

// Update is a bounded serializable progress event. Recoverable implementations
// must assign a non-empty, lifetime-stable EventID and replay updates in the
// same logical order after recovery.
type Update struct {
	EventID  string            `json:"event_id,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ToolStreamEvent is the framework-owned model-facing NDJSON envelope.
type ToolStreamEvent struct {
	Type        string                `json:"type"`
	TaskID      string                `json:"task_id,omitempty"`
	Status      backgroundtask.Status `json:"status,omitempty"`
	Description string                `json:"description,omitempty"`
	Output      any                   `json:"output,omitempty"`
	Error       string                `json:"error,omitempty"`
	Update      *Update               `json:"update,omitempty"`
}
