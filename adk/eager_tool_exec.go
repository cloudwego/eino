/*
 * Copyright 2025 CloudWeGo Authors
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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type eagerToolResult struct {
	sOutput         *schema.StreamReader[string]
	enhancedSOutput *schema.StreamReader[*schema.ToolResult]
	useEnhanced     bool
	err             error
}

type eagerCoord struct {
	mu      sync.Mutex
	results map[string]*eagerToolResult
	doneCh  chan struct{}
	once    sync.Once
	aborted bool
}

func newEagerCoord() *eagerCoord {
	return &eagerCoord{
		results: make(map[string]*eagerToolResult),
		doneCh:  make(chan struct{}),
	}
}

func (c *eagerCoord) abort() {
	c.mu.Lock()
	c.aborted = true
	c.mu.Unlock()
}

func (c *eagerCoord) isAborted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.aborted
}

func (c *eagerCoord) storeResult(callID string, result *eagerToolResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[callID] = result
}

func (c *eagerCoord) markDone() {
	c.once.Do(func() {
		close(c.doneCh)
	})
}

func (c *eagerCoord) waitDone(ctx context.Context) {
	select {
	case <-c.doneCh:
	case <-ctx.Done():
	}
}

// collectResults returns the results of all eagerly executed tools as
// StreamReader maps. If any tool failed, returns the first error encountered.
// Design: when multiple tools fail, the reported error is non-deterministic
// (Go map iteration order). This is acceptable because the error triggers
// ToolsNode-level failure regardless of which specific tool is reported, and
// the caller (ToolsNode) does not branch on error message content.
func (c *eagerCoord) collectResults() (
	executedStreamTools map[string]*schema.StreamReader[string],
	executedEnhancedStreamTools map[string]*schema.StreamReader[*schema.ToolResult],
	err error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.aborted {
		return nil, nil, nil
	}

	for callID, result := range c.results {
		if result.err != nil {
			return nil, nil, fmt.Errorf("eager tool call %s failed: %w", callID, result.err)
		}
		if result.useEnhanced {
			if executedEnhancedStreamTools == nil {
				executedEnhancedStreamTools = make(map[string]*schema.StreamReader[*schema.ToolResult])
			}
			executedEnhancedStreamTools[callID] = result.enhancedSOutput
		} else {
			if executedStreamTools == nil {
				executedStreamTools = make(map[string]*schema.StreamReader[string])
			}
			executedStreamTools[callID] = result.sOutput
		}
	}

	return executedStreamTools, executedEnhancedStreamTools, nil
}

type eagerCoordHolder struct {
	mu    sync.Mutex
	coord *eagerCoord
}

func (h *eagerCoordHolder) Store(c *eagerCoord) {
	h.mu.Lock()
	h.coord = c
	h.mu.Unlock()
}

func (h *eagerCoordHolder) Load() *eagerCoord {
	h.mu.Lock()
	c := h.coord
	h.mu.Unlock()
	return c
}

type eagerToolExecutorMiddleware[M MessageType] struct {
	toolsNode           *compose.ToolsNode
	toolNodeKey         string
	coordPtr            *eagerCoordHolder
	executeSequentially bool
}

func (e *eagerToolExecutorMiddleware[M]) runEager(ctx context.Context, stream *schema.StreamReader[M], coord *eagerCoord) {
	defer stream.Close()
	defer coord.markDone()

	accumulators := map[int]*toolCallAccumulator{}
	dispatched := map[string]bool{}
	// lastNilIdx tracks the current accumulator index for tool call chunks with
	// nil Index. Unlike concatToolCalls (which treats nil-Index chunks as
	// standalone), eager execution receives chunks incrementally and must route
	// continuation chunks (no ID, nil Index) to the correct accumulator. When a
	// new tool call ID appears on a nil-Index chunk, we allocate a new
	// accumulator and update lastNilIdx so subsequent continuation chunks
	// route there.
	lastNilIdx := 0
	var wg sync.WaitGroup

	// Design: dispatched tool goroutines use the parent ctx, not a dedicated
	// cancellable context. On abort (stream error), already-running tools
	// continue to completion but their results are discarded (collectResults
	// returns nil when aborted). This is acceptable because:
	// 1. Tools that are context-aware will exit promptly via ctx.Done()
	// 2. Adding a cancel per goroutine increases complexity for minimal benefit
	//    since abort → retry replaces the coord entirely
	dispatchTool := func(tc schema.ToolCall) {
		if coord.isAborted() {
			coord.storeResult(tc.ID, &eagerToolResult{err: context.Canceled})
			return
		}
		if e.executeSequentially {
			result := e.executeSingleTool(ctx, tc, coord)
			coord.storeResult(tc.ID, result)
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result := e.executeSingleTool(ctx, tc, coord)
				coord.storeResult(tc.ID, result)
			}()
		}
	}

	for {
		chunk, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			coord.abort()
			wg.Wait()
			return
		}

		for _, tc := range extractToolCalls(chunk) {
			if tc.ID != "" && tc.Function.Name != "" && isArgsComplete(tc.Function.Arguments) {
				if !dispatched[tc.ID] {
					dispatched[tc.ID] = true
					dispatchTool(tc)
				}
				continue
			}

			idx := derefIndex(tc.Index)
			if tc.Index == nil {
				if tc.ID != "" {
					if existing, ok := accumulators[lastNilIdx]; ok && existing.id != "" && existing.id != tc.ID {
						lastNilIdx = len(accumulators)
					}
				}
				idx = lastNilIdx
			}
			acc := getOrCreateAccumulator(accumulators, idx)
			if mergeErr := acc.merge(tc); mergeErr != nil {
				coord.abort()
				wg.Wait()
				return
			}

			if !dispatched[acc.id] && acc.id != "" && isArgsComplete(acc.args.String()) {
				dispatched[acc.id] = true
				dispatchTool(acc.toToolCall())
			}
		}
	}

	for _, acc := range accumulators {
		if !dispatched[acc.id] && acc.id != "" && acc.name != "" && isArgsComplete(acc.args.String()) {
			dispatched[acc.id] = true
			dispatchTool(acc.toToolCall())
		}
	}

	wg.Wait()
}

func (e *eagerToolExecutorMiddleware[M]) executeSingleTool(ctx context.Context, tc schema.ToolCall, coord *eagerCoord) *eagerToolResult {
	if coord.isAborted() {
		return &eagerToolResult{err: context.Canceled}
	}

	toolNodeCtx := compose.AppendAddressSegment(ctx, compose.AddressSegmentNode, e.toolNodeKey)

	sOutput, enhancedSOutput, useEnhanced, err := compose.InternalStreamSingleToolCall(e.toolsNode, toolNodeCtx, tc)
	if err != nil {
		return &eagerToolResult{err: err}
	}

	return &eagerToolResult{
		sOutput:         sOutput,
		enhancedSOutput: enhancedSOutput,
		useEnhanced:     useEnhanced,
	}
}

func extractToolCalls[M MessageType](msg M) []schema.ToolCall {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.ToolCalls
	case *schema.AgenticMessage:
		var tcs []schema.ToolCall
		for _, block := range v.ContentBlocks {
			if block.Type != schema.ContentBlockTypeFunctionToolCall || block.FunctionToolCall == nil {
				continue
			}
			tcs = append(tcs, schema.ToolCall{
				ID: block.FunctionToolCall.CallID,
				Function: schema.FunctionCall{
					Name:      block.FunctionToolCall.Name,
					Arguments: block.FunctionToolCall.Arguments,
				},
				Extra: block.Extra,
			})
		}
		return tcs
	}
	return nil
}

type toolCallAccumulator struct {
	id    string
	name  string
	args  strings.Builder
	typ   string
	extra map[string]any
}

// merge accumulates a tool call chunk into the accumulator.
// Conflict detection (different ID/Type/Name for the same accumulator) matches
// the behavior of concatToolCalls in schema/message.go. Under normal model
// streaming, conflicts should never occur for the same Index, but we detect
// them defensively.
func (a *toolCallAccumulator) merge(tc schema.ToolCall) error {
	if tc.ID != "" {
		if a.id == "" {
			a.id = tc.ID
		} else if a.id != tc.ID {
			return fmt.Errorf("conflicting tool call ID in same accumulator: %q vs %q", a.id, tc.ID)
		}
	}
	if tc.Function.Name != "" {
		if a.name == "" {
			a.name = tc.Function.Name
		} else if a.name != tc.Function.Name {
			return fmt.Errorf("conflicting tool call name in same accumulator: %q vs %q", a.name, tc.Function.Name)
		}
	}
	if tc.Type != "" {
		if a.typ == "" {
			a.typ = tc.Type
		} else if a.typ != tc.Type {
			return fmt.Errorf("conflicting tool call type in same accumulator: %q vs %q", a.typ, tc.Type)
		}
	}
	if tc.Extra != nil && a.extra == nil {
		a.extra = tc.Extra
	}
	if tc.Function.Arguments != "" {
		a.args.WriteString(tc.Function.Arguments)
	}
	return nil
}

func (a *toolCallAccumulator) toToolCall() schema.ToolCall {
	return schema.ToolCall{
		ID:    a.id,
		Type:  a.typ,
		Extra: a.extra,
		Function: schema.FunctionCall{
			Name:      a.name,
			Arguments: a.args.String(),
		},
	}
}

func getOrCreateAccumulator(m map[int]*toolCallAccumulator, idx int) *toolCallAccumulator {
	if acc, ok := m[idx]; ok {
		return acc
	}
	acc := &toolCallAccumulator{}
	m[idx] = acc
	return acc
}

func derefIndex(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func isArgsComplete(args string) bool {
	if args == "" {
		return false
	}
	args = strings.TrimSpace(args)
	if !strings.HasPrefix(args, "{") {
		return false
	}
	return json.Valid([]byte(args))
}
