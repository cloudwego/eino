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

package dream

// ErrorStage identifies the dream processing stage that produced a non-fatal error
// reported through Config.OnError. Dream errors are best-effort: a failure in one
// stage is surfaced through OnError and the run continues or backs off rather than
// aborting the host agent. Compare against the OnErrorStage* constants to branch on
// what went wrong.
type ErrorStage string

// OnErrorStage constants. These values are stable identifiers used to report
// best-effort failures through Config.OnError.
const (
	// OnErrorStageInit is reported during initialization for non-fatal configuration
	// warnings. The most common case is falling back to the in-process KVStore
	// (errLocalKVStoreSingleProcess) when Config.Store is nil; dreams still run, but
	// cross-instance coordination is unavailable.
	OnErrorStageInit ErrorStage = "init"

	// OnErrorStageRecordTouch is reported when recording the current session into the
	// touched-session set fails. The schedule trigger may miss this session as a
	// result, but the run is otherwise unaffected.
	OnErrorStageRecordTouch ErrorStage = "record_touch"

	// OnErrorStageRunDream is reported when a scheduled (middleware-triggered) dream
	// run fails. The scheduler records the failure, backs off, and eventually advances
	// the window after MaxConsecutiveFailures so the same sessions are not replayed
	// forever.
	OnErrorStageRunDream ErrorStage = "run_dream"

	// OnErrorStageSeedStaging is reported when seeding the staging working copy from
	// the input memory directory fails (for example a brand-new, empty memory
	// directory). The dream proceeds against whatever was seeded.
	OnErrorStageSeedStaging ErrorStage = "seed_staging"

	// OnErrorStagePromote is reported around promotion of the staged result to the
	// output directory. It carries errPromoteInPlaceBestEffort as a warning when the
	// output directory equals the input directory (a non-atomic, in-place overwrite).
	OnErrorStagePromote ErrorStage = "promote"

	// OnErrorStagePersistJob is reported when writing a Job lifecycle record to
	// the store fails. The run continues, but GetDreamStatus may not reflect the
	// latest status.
	OnErrorStagePersistJob ErrorStage = "persist_job"

	// OnErrorStageCleanup is reported when removing the staging directory through the
	// configured Shell fails after a successful promotion. The staging directory is
	// left in place; it is otherwise harmless.
	OnErrorStageCleanup ErrorStage = "cleanup_staging"

	// OnErrorStageToolCall is reported when a tool call inside the dream agent fails.
	// The error is turned into a message and fed back to the model so it can recover
	// (retry with corrected arguments or a different tool); the run is not aborted.
	// It is reported for observability only.
	OnErrorStageToolCall ErrorStage = "tool_call"
)
