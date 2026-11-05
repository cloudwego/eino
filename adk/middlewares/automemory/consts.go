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

import "time"

const (
	// candidateGlobPattern matches topic files under the memory directory.
	candidateGlobPattern = "**/*.md"

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

	// Extraction rounds are model calls over a long transcript plus one memory
	// filesystem round, budgeted here to derive the lock TTL.
	extractionTimePerTurn = 2 * time.Minute
	minExtractionLockTTL  = 5 * time.Minute
	maxExtractionLockTTL  = 30 * time.Minute
	// A pending snapshot must outlive one full lease: it is stashed when the lock
	// is contended and drained only after the current holder releases or expires.
	pendingSnapshotLockFactor = 2
)

// extractionLockTTL derives the extraction lock TTL from the extractor's tool-call
// budget. Locks are never renewed, so the TTL has to cover one complete drain.
func extractionLockTTL(maxTurns int) time.Duration {
	ttl := extractionTimePerTurn * time.Duration(maxTurns)
	if ttl < minExtractionLockTTL {
		return minExtractionLockTTL
	}
	if ttl > maxExtractionLockTTL {
		return maxExtractionLockTTL
	}
	return ttl
}

func pendingSnapshotTTL(lockTTL time.Duration) time.Duration {
	return pendingSnapshotLockFactor * lockTTL
}

// ErrorStage error stage during auto memory processing
type ErrorStage string

// OnError stage constants. These values are stable identifiers used to report
// best-effort failures through Config.OnError.
const (
	OnErrorStageTopicSelectionSync    ErrorStage = "topic_selection_sync"
	OnErrorStageTopicSelectionAsync   ErrorStage = "topic_selection_async"
	OnErrorStageRenderInstruction     ErrorStage = "render_instruction"
	OnErrorStageReadMemoryIndex       ErrorStage = "read_memory_index"
	OnErrorStageResolveSessionID      ErrorStage = "resolve_session_id"
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
