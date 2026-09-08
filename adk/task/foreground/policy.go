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

// Package foreground defines public policy inputs for task foreground projection.
package foreground

import (
	"context"
	"time"
)

const DefaultTimeoutMs = 120_000

// CandidateInfo describes a task whose foreground projection may detach.
type CandidateInfo struct {
	TaskID      string
	Kind        string
	Description string
	OutputFile  string
	StartedAt   time.Time
	Elapsed     time.Duration
}

// CallerAbortInfo describes why a foreground observer stopped waiting.
type CallerAbortInfo struct {
	Candidate CandidateInfo
	Err       error
}

// ShouldAutoBackground decides whether timeout detaches a foreground projection.
type ShouldAutoBackground func(context.Context, *CandidateInfo) bool

// ShouldCancelOnCallerAbort decides whether caller abort also cancels the task.
type ShouldCancelOnCallerAbort func(context.Context, *CallerAbortInfo) bool
