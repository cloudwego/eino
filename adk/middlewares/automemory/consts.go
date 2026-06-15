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

	defaultTopicTopK     = 5
	defaultTopicMaxLines = 200
	defaultTopicMaxBytes = 4 * 1024

	defaultMemoryWriteMaxTurns = 5

	topicSelectionToolName = "select_memories"
)

// OnError stage constants. These values are stable identifiers used to report
// best-effort failures through Config.OnError.
const (
	OnErrorStageTopicSelectionSync    = "topic_selection_sync"
	OnErrorStageTopicSelectionAsync   = "topic_selection_async"
	OnErrorStageRenderInstruction     = "render_instruction"
	OnErrorStageResolveSessionID      = "resolve_session_id"
	OnErrorStageMemoryWriteSync       = "memory_write_sync"
	OnErrorStageSnapshotMarshal       = "snapshot_marshal"
	OnErrorStageAcquireExtractionLock = "acquire_extraction_lock"
	OnErrorStageStashPendingSnapshot  = "stash_pending_snapshot"
	OnErrorStageReleaseExtractionLock = "release_extraction_lock"
	OnErrorStageDecodePendingSnapshot = "decode_pending_snapshot"
	OnErrorStageMemoryWriteAsync      = "memory_write_async"
	OnErrorStageLoadPendingSnapshot   = "load_pending_snapshot"
)
