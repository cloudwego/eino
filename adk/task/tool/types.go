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

// Package tool adapts explicitly capable external tools to durable tasks.
package tool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

const (
	// ResumeInputKind identifies mailbox input for a waiting managed tool.
	ResumeInputKind = "eino.tool.resume"
)

var (
	// ErrResumeInputRejected reports that resume data is invalid for the
	// current InputRequest. ResumableTool.Resume must return this
	// error before applying side effects and with a nil Run. The framework
	// leaves the task waiting on the same request.
	ErrResumeInputRejected = errors.New(
		"task/tool: resume input rejected",
	)
)

const (
	// ExecutorKey identifies non-recoverable managed tools.
	ExecutorKey = "eino.dev/task-tool"
	// RecoverableExecutorKey identifies managed tools that support Worker handoff.
	RecoverableExecutorKey = "eino.dev/recoverable-task-tool"
)

// Tool starts one logical external operation. ValidateArguments must
// be repeatable and side-effect free. Start receives the Eino task ID before
// any external side effect occurs. Attempt 0 is a parent-owned foreground
// invocation with no persisted task or store-authorized attempt; background
// attempts start at 1 and must be preceded by Manager.Submit. InputPreparer
// supports ordinary Runner interruption before durable task creation.
// ResumableTool supports durable input requests after creation. An
// error from Start is a durable execution failure for background attempts and a
// model-visible foreground failure for Attempt 0. For recoverable tools, Eino
// commits StartResult.Checkpoint and the external-start boundary before calling
// Run.Wait only for background attempts.
type Tool interface {
	ValidateArguments(arguments string) error
	Start(context.Context, *StartRequest) (*StartResult, error)
}

// InputPreparer optionally completes or rewrites tool arguments before durable
// task creation. The managed-tool wrapper calls PrepareInput synchronously in
// the parent Runner tool invocation, before task ID allocation, output
// reservation, or persistence. Implementations may use components/tool
// StatefulInterrupt, GetInterruptState, and GetResumeContext normally.
//
// PrepareInput may be re-entered by Runner and must not start the external
// operation or perform non-idempotent side effects. Its non-empty result must
// be the final JSON arguments: the framework validates, persists, and supplies
// them to policy callbacks and Start or Recover.
type InputPreparer interface {
	PrepareInput(
		ctx context.Context,
		arguments string,
	) (preparedArguments string, err error)
}

// RecoverableTool reconstructs the same logical operation after a
// Worker loss or graceful yield. Implementations must make Start idempotent by
// TaskID because Worker loss may occur after the external start but before Eino
// persists StartResult.Checkpoint and the started marker.
type RecoverableTool interface {
	Tool
	Recover(context.Context, *RecoverRequest) (Run, error)
}

// ResumableTool supports durable input requests after the operation
// has started. Resume must apply the same RequestID and Data idempotently:
// Worker loss may cause the framework to repeat the call on a later attempt.
//
// Implementations keep operation state in their external durable backend. The
// framework persists opaque tool checkpoints and the current InputRequest but
// never interprets tool checkpoint bytes.
type ResumableTool interface {
	RecoverableTool
	// Resume validates and applies input atomically from the framework's
	// perspective. Invalid input returns ErrResumeInputRejected before any side
	// effect; other errors are durable execution failures.
	Resume(context.Context, *ResumeRequest) (Run, error)
}

// ForegroundHandoffTool can adopt a running Attempt 0 foreground operation into
// background ownership without repeating its side effects.
type ForegroundHandoffTool interface {
	Tool
	Adopt(context.Context, *AdoptRequest) (*AdoptResult, error)
}

// StartRequest describes an initial external-operation start.
type StartRequest struct {
	TaskID    string
	Arguments string
	Attempt   int64
}

// StartResult contains the attempt-local Run and the initial opaque tool
// checkpoint. For Attempt >= 1, Eino persists an independently owned copy
// before calling Run.Wait. For Attempt 0, the checkpoint is held in memory until
// foreground handoff or stored in the parent Runner checkpoint for foreground
// waiting-input. Checkpoint may be empty when TaskID alone is sufficient to
// recover and must be empty for a non-recoverable Tool.
type StartResult struct {
	Run        Run
	Checkpoint []byte
}

// AdoptRequest transfers an Attempt 0 foreground operation into background
// ownership. Run must be the live handle returned by the matching Start call.
type AdoptRequest struct {
	TaskID         string
	Arguments      string
	Run            Run
	ToolCheckpoint []byte
}

// AdoptResult contains the background-owned run handle and checkpoint to use
// for the first durable attempt after handoff.
type AdoptResult struct {
	Run            Run
	ToolCheckpoint []byte
}

// RecoverRequest describes reconstruction of an existing logical operation.
// Checkpoint is an independently owned copy of the latest opaque tool
// checkpoint.
type RecoverRequest struct {
	TaskID     string
	Arguments  string
	Attempt    int64
	Checkpoint []byte
}

// ResumeRequest applies durable external input to a waiting logical operation.
// RequestID identifies the exact InputRequest being answered. Data is opaque to
// the framework and may be empty. Checkpoint is an independently owned copy of
// the checkpoint persisted at that waiting boundary. A later attempt may replay
// the same request.
type ResumeRequest struct {
	TaskID     string
	Arguments  string
	Attempt    int64
	RequestID  string
	Data       []byte
	Checkpoint []byte
}

// Run is an attempt-local handle for one logical external operation. Canceling
// Wait stops observation only. Stop requests logical cancellation and must be
// safe under repeated or concurrent calls.
type Run interface {
	Wait(context.Context) (*Outcome, error)
	Stop(context.Context) error
}

// UpdateSource optionally exposes replayable incremental updates. The framework
// calls Updates once per Run, owns and closes the returned reader, and expects
// it to close shortly after Wait reaches a terminal outcome. Recovery starts at
// the beginning of the replayable history; EventID deduplication removes repeats.
type UpdateSource interface {
	Updates() *schema.StreamReader[*Update]
}

// InputRequest describes the current durable question from a managed tool.
// ID must be stable for this question across recovery. Data must contain one
// valid JSON value and is embedded unchanged in model-facing responses.
type InputRequest struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Outcome is the authoritative logical-operation result. Completed outcomes
// may contain Data and no Error. Failed outcomes require Error and no Data.
// Canceled outcomes may contain Error and no Data. Waiting-input outcomes set
// InputRequest and may set Checkpoint; a non-empty Checkpoint replaces the
// latest tool checkpoint while an empty value retains it. Other statuses must
// leave InputRequest and Checkpoint empty. Waiting input is supported only by
// ResumableTool.
type Outcome struct {
	Status       background.Status
	Data         []byte
	Error        string
	InputRequest *InputRequest
	// Checkpoint replaces the latest opaque tool checkpoint when Status is
	// StatusWaitingInput and this field is non-empty. Empty retains the latest
	// checkpoint. Other statuses must leave it empty.
	Checkpoint []byte
}

// Update is a bounded serializable progress event. Data is limited to 256 KiB,
// Kind to 128 bytes, and Metadata to 32 entries with keys and values at most
// 1024 bytes each. Recoverable implementations must assign a non-empty,
// lifetime-stable EventID and replay updates in logical order. For plain tools,
// the framework may generate an ID without mutating this value. EventID is not
// an ordering key or pagination cursor.
type Update struct {
	EventID  string            `json:"event_id,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Data     []byte            `json:"data,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ManagedToolResponseEventType identifies one model-facing managed-tool
// response variant.
type ManagedToolResponseEventType string

const (
	// ManagedToolResponseEventUpdate carries one live progress Update. All other
	// ManagedToolResponseEvent fields are empty.
	ManagedToolResponseEventUpdate ManagedToolResponseEventType = "update"
	// ManagedToolResponseEventLaunchResult carries the task launch or foreground result.
	// Update is nil; the remaining fields describe the task and its current or
	// terminal outcome.
	ManagedToolResponseEventLaunchResult ManagedToolResponseEventType = "launch_result"
	// ManagedToolResponseEventForegroundResult carries a synchronous foreground
	// result. No TaskID is set because no task was persisted.
	ManagedToolResponseEventForegroundResult ManagedToolResponseEventType = "foreground_result"
)

// ManagedToolResponseEvent is the framework-owned model-facing text control
// envelope. The enhanced managed-tool wrapper encodes it as the first text part
// of every ToolResult; streaming uses one newline-terminated record per chunk.
// Type determines the legal variant: update events set only Update, while
// launch-result events set task identity, status, description, and either a
// waiting InputRequest or terminal Output or Error.
type ManagedToolResponseEvent struct {
	Type         ManagedToolResponseEventType `json:"type"`
	TaskID       string                       `json:"task_id,omitempty"`
	Status       background.Status            `json:"status,omitempty"`
	Description  string                       `json:"description,omitempty"`
	Output       any                          `json:"output,omitempty"`
	Error        string                       `json:"error,omitempty"`
	InputRequest *InputRequest                `json:"input_request,omitempty"`
	Update       *Update                      `json:"update,omitempty"`
}
