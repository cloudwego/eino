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

package adk

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/cloudwego/eino/compose"
	checkpointinternal "github.com/cloudwego/eino/internal/checkpoint"
	"github.com/cloudwego/eino/schema"
)

const (
	projectionToolResultKindString   = "string"
	projectionToolResultKindEnhanced = "enhanced"
	infoTargetRerunToolResult        = "rerun_tool_result"
	infoTargetContextToolResult      = "context_tool_result"
)

type checkpointToolResultSourceV1 struct {
	Kind        string
	GraphPath   []string
	InterruptID string
	ToolCallID  string
	Digest      string
}

// infoToolResultProjectionV1 maps one omitted result back to either a rerun
// entry or an interrupt-context entry. Target selects the applicable fields.
type infoToolResultProjectionV1 struct {
	Target        string
	SubGraphPath  []string
	ContextIndex  int
	ParentDepth   int
	RerunExtraKey string
	ToolCallID    string
	Source        checkpointToolResultSourceV1
}

type canonicalCheckpointToolResult struct {
	source   checkpointToolResultSourceV1
	text     string
	enhanced *schema.ToolResult
}

func (i *checkpointProjectionIndex) addCheckpointToolResults(path []string,
	interruptID string, value any) {
	standard, enhanced, ok := checkpointToolExecutionMaps(value)
	if !ok {
		return
	}
	if i.toolResultsByCallID == nil {
		i.toolResultsByCallID = make(map[string][]canonicalCheckpointToolResult)
	}
	for callID, result := range standard {
		digest := sha256.Sum256([]byte(result))
		i.toolResultsByCallID[callID] = append(i.toolResultsByCallID[callID],
			canonicalCheckpointToolResult{
				source: checkpointToolResultSourceV1{
					Kind:        projectionToolResultKindString,
					GraphPath:   append([]string(nil), path...),
					InterruptID: interruptID,
					ToolCallID:  callID,
					Digest:      hex.EncodeToString(digest[:]),
				},
				text: result,
			})
	}
	for callID, result := range enhanced {
		digest, ok := projectionMessageDigest(result)
		if !ok {
			continue
		}
		i.toolResultsByCallID[callID] = append(i.toolResultsByCallID[callID],
			canonicalCheckpointToolResult{
				source: checkpointToolResultSourceV1{
					Kind:        projectionToolResultKindEnhanced,
					GraphPath:   append([]string(nil), path...),
					InterruptID: interruptID,
					ToolCallID:  callID,
					Digest:      digest,
				},
				enhanced: result,
			})
	}
}

func checkpointToolExecutionMaps(value any) (map[string]string,
	map[string]*schema.ToolResult, bool) {
	state, ok := value.(*checkpointinternal.ToolsNodeInterruptStateV1)
	if !ok || state == nil {
		return nil, nil, false
	}
	return state.ExecutedTools, state.ExecutedEnhancedTools, true
}

func projectInfoToolResults(extra *compose.ToolsInterruptAndRerunExtra,
	target infoProjectionTarget, index *checkpointProjectionIndex,
	projection *checkpointProjectionV1) {
	if extra == nil {
		return
	}
	targetKind, ok := infoTargetForToolResult(target.kind)
	if !ok {
		return
	}
	standardIDs := sortedStringKeys(extra.ExecutedTools)
	for _, callID := range standardIDs {
		if callID == "" {
			continue
		}
		if _, conflicts := extra.ExecutedEnhancedTools[callID]; conflicts {
			continue
		}
		result := extra.ExecutedTools[callID]
		source, ok := index.sourceForStandardToolResult(callID, result)
		if !ok {
			continue
		}
		delete(extra.ExecutedTools, callID)
		projection.ToolResultRefs = append(projection.ToolResultRefs,
			newInfoToolResultProjection(targetKind, target, callID, source))
	}
	enhancedIDs := sortedStringKeys(extra.ExecutedEnhancedTools)
	for _, callID := range enhancedIDs {
		if callID == "" {
			continue
		}
		if _, conflicts := extra.ExecutedTools[callID]; conflicts {
			continue
		}
		result := extra.ExecutedEnhancedTools[callID]
		source, ok := index.sourceForEnhancedToolResult(callID, result)
		if !ok {
			continue
		}
		delete(extra.ExecutedEnhancedTools, callID)
		projection.ToolResultRefs = append(projection.ToolResultRefs,
			newInfoToolResultProjection(targetKind, target, callID, source))
	}
}

func newInfoToolResultProjection(targetKind string, target infoProjectionTarget,
	callID string, source checkpointToolResultSourceV1) infoToolResultProjectionV1 {
	return infoToolResultProjectionV1{
		Target:        targetKind,
		SubGraphPath:  append([]string(nil), target.path...),
		ContextIndex:  target.contextIndex,
		ParentDepth:   target.parentDepth,
		RerunExtraKey: target.rerunKey,
		ToolCallID:    callID,
		Source:        source,
	}
}

func infoTargetForToolResult(target string) (string, bool) {
	if target == infoTargetRerunToolCalls {
		return infoTargetRerunToolResult, true
	}
	if target == infoTargetContextStateMessage {
		return infoTargetContextToolResult, true
	}
	return "", false
}

func (i *checkpointProjectionIndex) sourceForStandardToolResult(
	callID, result string) (checkpointToolResultSourceV1, bool) {
	for _, candidate := range i.sortedToolResultCandidates(callID) {
		if candidate.source.Kind == projectionToolResultKindString && candidate.text == result {
			return candidate.source, true
		}
	}
	return checkpointToolResultSourceV1{}, false
}

func (i *checkpointProjectionIndex) sourceForEnhancedToolResult(callID string,
	result *schema.ToolResult) (checkpointToolResultSourceV1, bool) {
	for _, candidate := range i.sortedToolResultCandidates(callID) {
		if candidate.source.Kind == projectionToolResultKindEnhanced &&
			reflect.DeepEqual(candidate.enhanced, result) {
			return candidate.source, true
		}
	}
	return checkpointToolResultSourceV1{}, false
}

func (i *checkpointProjectionIndex) sortedToolResultCandidates(
	callID string) []canonicalCheckpointToolResult {
	candidates := append([]canonicalCheckpointToolResult(nil), i.toolResultsByCallID[callID]...)
	sort.Slice(candidates, func(left, right int) bool {
		leftKey := fmt.Sprintf("%q/%s/%s", candidates[left].source.GraphPath,
			candidates[left].source.InterruptID, candidates[left].source.Kind)
		rightKey := fmt.Sprintf("%q/%s/%s", candidates[right].source.GraphPath,
			candidates[right].source.InterruptID, candidates[right].source.Kind)
		return leftKey < rightKey
	})
	return candidates
}

func hydrateInterruptInfoToolResults(info *InterruptInfo, refs []infoToolResultProjectionV1,
	expectedCount int, index *checkpointProjectionIndex) error {
	if len(refs) != expectedCount {
		return fmt.Errorf("checkpoint projection tool result reference count mismatch: got %d, want %d",
			len(refs), expectedCount)
	}
	if len(refs) == 0 {
		return nil
	}
	if info == nil {
		return errors.New("checkpoint projection tool result interrupt info is missing")
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil || chatModelInfo.Info == nil {
		return fmt.Errorf("checkpoint projection tool result interrupt info has invalid type %T", info.Data)
	}
	return hydrateComposeInterruptInfoToolResults(chatModelInfo.Info, refs, expectedCount, index)
}

func hydrateComposeInterruptInfoToolResults(info *compose.InterruptInfo,
	refs []infoToolResultProjectionV1, expectedCount int,
	index *checkpointProjectionIndex) error {
	if len(refs) != expectedCount {
		return fmt.Errorf("checkpoint projection tool result reference count mismatch: got %d, want %d",
			len(refs), expectedCount)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.ParentDepth < 0 || ref.ToolCallID == "" {
			return errors.New("checkpoint projection has invalid tool result coordinates")
		}
		key := fmt.Sprintf("%s/%q/%d/%d/%s/%s", ref.Target, ref.SubGraphPath,
			ref.ContextIndex, ref.ParentDepth, ref.RerunExtraKey, ref.ToolCallID)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("checkpoint projection has duplicate tool result target %q", key)
		}
		seen[key] = struct{}{}

		targetInfo, err := composeInterruptInfoAtPath(info, ref.SubGraphPath)
		if err != nil {
			return err
		}
		var target any
		switch ref.Target {
		case infoTargetRerunToolResult:
			if ref.ContextIndex != -1 || ref.RerunExtraKey == "" {
				return errors.New("checkpoint projection has invalid rerun tool result target")
			}
			target = targetInfo.RerunNodesExtra[ref.RerunExtraKey]
		case infoTargetContextToolResult:
			if ref.ContextIndex < 0 {
				return errors.New("checkpoint projection has invalid context tool result target")
			}
			contextInfo, err := interruptContextAt(targetInfo, ref.ContextIndex, ref.ParentDepth)
			if err != nil {
				return err
			}
			target = contextInfo.Info
		default:
			return fmt.Errorf("checkpoint projection has unsupported tool result target %q", ref.Target)
		}
		extra, ok := target.(*compose.ToolsInterruptAndRerunExtra)
		if !ok || extra == nil {
			return fmt.Errorf("checkpoint projection tool result target has invalid type %T", target)
		}
		if err := hydrateInfoToolResult(extra, ref, index); err != nil {
			return err
		}
	}
	return nil
}

func hydrateInfoToolResult(extra *compose.ToolsInterruptAndRerunExtra,
	ref infoToolResultProjectionV1, index *checkpointProjectionIndex) error {
	if ref.ToolCallID != ref.Source.ToolCallID {
		return fmt.Errorf("checkpoint projection tool call ID %q does not match source %q",
			ref.ToolCallID, ref.Source.ToolCallID)
	}
	candidate, err := index.toolResult(ref.Source)
	if err != nil {
		return err
	}
	if _, exists := extra.ExecutedTools[ref.ToolCallID]; exists {
		return fmt.Errorf("checkpoint projection tool result target %q is already populated", ref.ToolCallID)
	}
	if _, exists := extra.ExecutedEnhancedTools[ref.ToolCallID]; exists {
		return fmt.Errorf("checkpoint projection tool result target %q is already populated", ref.ToolCallID)
	}
	switch ref.Source.Kind {
	case projectionToolResultKindString:
		if extra.ExecutedTools == nil {
			extra.ExecutedTools = make(map[string]string)
		}
		extra.ExecutedTools[ref.ToolCallID] = candidate.text
	case projectionToolResultKindEnhanced:
		if extra.ExecutedEnhancedTools == nil {
			extra.ExecutedEnhancedTools = make(map[string]*schema.ToolResult)
		}
		cloned, err := cloneToolResultForProjection(candidate.enhanced)
		if err != nil {
			return err
		}
		extra.ExecutedEnhancedTools[ref.ToolCallID] = cloned
	default:
		return fmt.Errorf("checkpoint projection has unsupported tool result kind %q", ref.Source.Kind)
	}
	return nil
}

func (i *checkpointProjectionIndex) toolResult(
	source checkpointToolResultSourceV1) (canonicalCheckpointToolResult, error) {
	for _, candidate := range i.toolResultsByCallID[source.ToolCallID] {
		if candidate.source.Kind == source.Kind &&
			candidate.source.InterruptID == source.InterruptID &&
			checkpointProjectionPathEqual(candidate.source.GraphPath, source.GraphPath) &&
			candidate.source.Digest == source.Digest {
			return candidate, nil
		}
	}
	return canonicalCheckpointToolResult{},
		fmt.Errorf("checkpoint projection tool result %q does not match metadata", source.ToolCallID)
}

func cloneToolResultForProjection(result *schema.ToolResult) (*schema.ToolResult, error) {
	if result == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(result); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint tool result: %w", err)
	}
	var cloned schema.ToolResult
	if err := gob.NewDecoder(&buf).Decode(&cloned); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint tool result: %w", err)
	}
	return &cloned, nil
}

func sortedStringKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
