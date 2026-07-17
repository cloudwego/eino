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

package automemory

const (
	// CandidateGlobPattern matches topic files under the memory directory.
	CandidateGlobPattern = "**/*.md"

	memoryIndexFileName = "MEMORY.md"

	defaultIndexMaxLines = 200
	defaultIndexMaxBytes = 4 * 1024

	defaultCandidateLimit       = 200
	defaultCandidatePreviewLine = 30

	defaultTopicTopK          = 5
	defaultTopicMaxLines      = 200
	defaultTopicMaxBytes      = 4 * 1024
	defaultTopicMaxTotalBytes = 16 * 1024

	defaultMemoryWriteMaxTurns = 5

	topicSelectionToolName = "select_memories"
)

// ErrorStage error stage during auto memory processing
type ErrorStage string

// OnError stage constants identify where an error occurred in the automemory lifecycle.
//
// The OnError callback's return value controls whether the middleware blocks (non-nil)
// or continues with degraded behavior (nil). However, for stages that fire in background
// goroutines or non-propagatable contexts, the return value is ignored — the error is
// reported for observability only.
//
// Stages where return value is honored (can block):
//   - OnErrorStageRenderInstruction (BeforeAgent)
//   - OnErrorStageReadMemoryIndex (BeforeAgent)
//   - OnErrorStageTopicSelectionSync (BeforeAgent)
//   - OnErrorStageTopicSelectionAsync (BeforeModelRewriteState)
//   - OnErrorStageMemoryWriteSync (AfterAgent)
//   - OnErrorStageSnapshotMarshal (AfterAgent)
//   - OnErrorStageAcquireExtractionLock (AfterAgent)
//
// Stages where return value is ignored (background/async, notification only):
//   - OnErrorStageStashPendingSnapshot
//   - OnErrorStageReleaseExtractionLock
//   - OnErrorStageDecodePendingSnapshot
//   - OnErrorStageMemoryWriteAsync
//   - OnErrorStageLoadPendingSnapshot
//   - OnErrorStageSendSessionEvent
const (
	OnErrorStageTopicSelectionSync    ErrorStage = "topic_selection_sync"
	OnErrorStageTopicSelectionAsync   ErrorStage = "topic_selection_async"
	OnErrorStageRenderInstruction     ErrorStage = "render_instruction"
	OnErrorStageReadMemoryIndex       ErrorStage = "read_memory_index"
	OnErrorStageMemoryWriteSync       ErrorStage = "memory_write_sync"
	OnErrorStageSnapshotMarshal       ErrorStage = "snapshot_marshal"
	OnErrorStageAcquireExtractionLock ErrorStage = "acquire_extraction_lock"
	OnErrorStageStashPendingSnapshot  ErrorStage = "stash_pending_snapshot"
	OnErrorStageReleaseExtractionLock ErrorStage = "release_extraction_lock"
	OnErrorStageDecodePendingSnapshot ErrorStage = "decode_pending_snapshot"
	OnErrorStageMemoryWriteAsync      ErrorStage = "memory_write_async"
	OnErrorStageLoadPendingSnapshot   ErrorStage = "load_pending_snapshot"
	OnErrorStageSendSessionEvent      ErrorStage = "send_session_event"
)
