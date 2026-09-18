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
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type composeCheckpointSizeMirror struct {
	State             any
	Inputs            map[string]any
	SubGraphs         map[string]*composeCheckpointSizeMirror
	InterruptID2State map[string]core.InterruptState
}

type checkpointSizeBreakdown struct {
	RunnerRawBytes             int
	RunCtxBytes                int
	LegacyInfoBytes            int
	OuterInterruptStateBytes   int
	ComposeStateBytes          int
	ComposeInputsBytes         int
	ComposeSubGraphsBytes      int
	ComposeInterruptStateBytes int
	AgentToolChildRunnerBytes  int
	RunCtxProjectionRefs       int
	InfoProjectionRefs         int
	InfoStateBytes             int
	InfoSubGraphsBytes         int
	InfoInterruptContextsBytes int
	InfoRerunExtraBytes        int
}

func gobEncodedSize(t *testing.T, value any) int {
	t.Helper()
	if value == nil {
		return 0
	}
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(value))
	return buf.Len()
}

func measureCheckpointSizeBreakdown(t *testing.T, raw []byte) checkpointSizeBreakdown {
	t.Helper()
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))

	breakdown := checkpointSizeBreakdown{
		RunnerRawBytes:           len(raw),
		RunCtxBytes:              gobEncodedSize(t, outer.RunCtx),
		LegacyInfoBytes:          gobEncodedSize(t, outer.Info),
		OuterInterruptStateBytes: gobEncodedSize(t, outer.InterruptID2State),
	}
	if outer.ProjectionV1 != nil {
		breakdown.RunCtxProjectionRefs = len(outer.ProjectionV1.RunCtxRefs)
		breakdown.InfoProjectionRefs = len(outer.ProjectionV1.InfoRefs)
	}
	if chatModelInfo, ok := outer.Info.Data.(*ChatModelAgentInterruptInfo); ok && chatModelInfo != nil {
		measureInterruptInfoBreakdown(t, chatModelInfo.Info, &breakdown)
	}
	for _, state := range outer.InterruptID2State {
		measureCheckpointStateValue(t, state.State, &breakdown)
	}
	return breakdown
}

func measureInterruptInfoBreakdown(t *testing.T, info *compose.InterruptInfo,
	breakdown *checkpointSizeBreakdown) {
	t.Helper()
	if info == nil {
		return
	}
	breakdown.InfoStateBytes += gobEncodedSize(t, info.State)
	breakdown.InfoSubGraphsBytes += gobEncodedSize(t, info.SubGraphs)
	breakdown.InfoInterruptContextsBytes += gobEncodedSize(t, info.InterruptContexts)
	breakdown.InfoRerunExtraBytes += gobEncodedSize(t, info.RerunNodesExtra)
	for _, interruptCtx := range info.InterruptContexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			if state, ok := current.Info.(*State); ok && state != nil {
				breakdown.InfoStateBytes += gobEncodedSize(t, state)
			}
			if projected, ok := current.Info.(*checkpointInterruptInfoPlaceholderV1); ok && projected != nil {
				measureInterruptInfoBreakdown(t, projected.Info, breakdown)
			}
		}
	}
	for _, sub := range info.SubGraphs {
		measureInterruptInfoBreakdown(t, sub, breakdown)
	}
}

func measureCheckpointStateValue(t *testing.T, value any, breakdown *checkpointSizeBreakdown) {
	t.Helper()
	switch state := value.(type) {
	case []byte:
		measureComposeCheckpointBytes(t, state, breakdown)
	case *agentToolInterruptStateV1:
		if state == nil {
			return
		}
		breakdown.AgentToolChildRunnerBytes += len(state.BridgeCheckpoint)
		var child serialization
		require.NoError(t, gob.NewDecoder(bytes.NewReader(state.BridgeCheckpoint)).Decode(&child))
		for _, childState := range child.InterruptID2State {
			measureCheckpointStateValue(t, childState.State, breakdown)
		}
	case *agentToolInterruptStateV2:
		if state == nil {
			return
		}
		breakdown.AgentToolChildRunnerBytes += len(state.BridgeCheckpoint)
		var child serialization
		require.NoError(t, gob.NewDecoder(bytes.NewReader(state.BridgeCheckpoint)).Decode(&child))
		for _, childState := range child.InterruptID2State {
			measureCheckpointStateValue(t, childState.State, breakdown)
		}
	}
}

func measureComposeCheckpointBytes(t *testing.T, data []byte, breakdown *checkpointSizeBreakdown) {
	t.Helper()
	var cp composeCheckpointSizeMirror
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&cp); err != nil {
		return
	}
	measureComposeCheckpoint(t, &cp, breakdown)
}

func measureComposeCheckpoint(t *testing.T, cp *composeCheckpointSizeMirror,
	breakdown *checkpointSizeBreakdown) {
	t.Helper()
	if cp == nil {
		return
	}
	breakdown.ComposeStateBytes += gobEncodedSize(t, cp.State)
	breakdown.ComposeInputsBytes += gobEncodedSize(t, cp.Inputs)
	breakdown.ComposeSubGraphsBytes += gobEncodedSize(t, cp.SubGraphs)
	breakdown.ComposeInterruptStateBytes += gobEncodedSize(t, cp.InterruptID2State)
	for _, state := range cp.InterruptID2State {
		measureCheckpointStateValue(t, state.State, breakdown)
	}
	for _, sub := range cp.SubGraphs {
		measureComposeCheckpoint(t, sub, breakdown)
	}
}

func restoredCheckpointPayloadMessages(t *testing.T, raw []byte, field string,
	payloadSize int) []canonicalCheckpointMessage {
	t.Helper()
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NoError(t, restoreRunnerCheckpointProjection(&outer))

	return checkpointPayloadMessages(t, &outer, field, payloadSize)
}

func projectedCheckpointPayloadMessages(t *testing.T, raw []byte, field string,
	payloadSize int) []canonicalCheckpointMessage {
	t.Helper()
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))

	return checkpointPayloadMessages(t, &outer, field, payloadSize)
}

func checkpointPayloadMessages(t *testing.T, outer *serialization, field string,
	payloadSize int) []canonicalCheckpointMessage {
	t.Helper()
	payload := strings.Repeat("x", payloadSize)
	var matches []canonicalCheckpointMessage
	if outer.RunCtx != nil && outer.RunCtx.RootInput != nil {
		for _, message := range outer.RunCtx.RootInput.Messages {
			if checkpointMessageHasPayload(message, field, payload) {
				matches = append(matches, canonicalCheckpointMessage{message: message})
			}
		}
	}

	sourceID := outer.InfoDataSourceInterruptID
	if outer.ProjectionV1 != nil {
		sourceID = outer.ProjectionV1.SourceInterruptID
	}
	require.NotEmpty(t, sourceID)
	source, exists := outer.InterruptID2State[sourceID]
	require.True(t, exists)
	sourceData, ok := source.State.([]byte)
	require.True(t, ok)
	index, err := buildCheckpointProjectionIndex(sourceData)
	require.NoError(t, err)

	for _, candidate := range index.allMessages() {
		if checkpointMessageHasPayload(candidate.message, field, payload) {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func requireCheckpointSemanticRestoreAndResume(t *testing.T, raw []byte,
	spec checkpointCompatFixture, interruptIDs, interruptAddresses []string) {
	t.Helper()
	require.Len(t, interruptIDs, 1)
	require.Len(t, interruptAddresses, 1)

	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), spec.Name, raw))
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent: newCheckpointCompatStableDepthAgent(t, spec.Depth,
			spec.PayloadField, spec.PayloadSize),
		EnableStreaming: spec.Streaming,
		CheckPointStore: store,
	})
	targets := make(map[string]any, len(interruptIDs))
	for _, interruptID := range interruptIDs {
		targets[interruptID] = "resumed"
	}
	iter, err := runner.ResumeWithParams(context.Background(), spec.Name,
		&ResumeParams{Targets: targets})
	require.NoError(t, err)
	requireCheckpointCompatResumeOutcome(t,
		collectCheckpointCompatResumeOutcome(t, iter), 0, nil)
}

func checkpointMessageHasPayload(message *schema.Message, field, payload string) bool {
	if message == nil {
		return false
	}
	switch field {
	case "user_query":
		return message.Role == schema.User && message.Content == payload
	case "content":
		return message.Role == schema.Assistant && message.Content == payload
	case "reasoning":
		return message.Role == schema.Assistant && message.ReasoningContent == payload
	case "tool_arguments":
		for _, call := range message.ToolCalls {
			if call.Function.Arguments == fmt.Sprintf(`{"request":%q}`, payload) {
				return true
			}
		}
	case "extra":
		return message.Extra["checkpoint_payload"] == payload
	case "multimodal":
		for _, part := range message.AssistantGenMultiContent {
			if part.Image != nil && part.Image.Base64Data != nil &&
				*part.Image.Base64Data == payload {
				return true
			}
		}
	}
	return false
}

func validateConstantStructuralGrowth(sizes []int, tolerance int) error {
	if len(sizes) < 3 || tolerance < 0 {
		return errors.New("constant-growth oracle requires at least three sizes and a non-negative tolerance")
	}
	minIncrement := sizes[1] - sizes[0]
	maxIncrement := minIncrement
	for depth := 2; depth < len(sizes); depth++ {
		increment := sizes[depth] - sizes[depth-1]
		if increment < minIncrement {
			minIncrement = increment
		}
		if increment > maxIncrement {
			maxIncrement = increment
		}
	}
	if minIncrement <= 0 {
		return fmt.Errorf("structural growth must be positive: minimum increment %d", minIncrement)
	}
	if maxIncrement-minIncrement > tolerance {
		return fmt.Errorf("structural increments vary by %d bytes, tolerance is %d",
			maxIncrement-minIncrement, tolerance)
	}
	return nil
}

func TestConstantStructuralGrowthOracle(t *testing.T) {
	require.NoError(t, validateConstantStructuralGrowth(
		[]int{1000, 2000, 3005, 3998, 5002}, 16))
	require.EqualError(t, validateConstantStructuralGrowth(
		[]int{1000, 2000, 4000, 7000, 11000}, 256),
		"structural increments vary by 3000 bytes, tolerance is 256")
	require.EqualError(t, validateConstantStructuralGrowth(
		[]int{1000, 2000, 4000, 8000, 16000}, 256),
		"structural increments vary by 7000 bytes, tolerance is 256")
}

func validateBoundedPayloadDeltaVariation(deltas []int, tolerance int) error {
	if len(deltas) < 2 || tolerance < 0 {
		return errors.New("payload delta oracle requires at least two deltas and a non-negative tolerance")
	}
	minDelta := deltas[0]
	maxDelta := minDelta
	for _, delta := range deltas[1:] {
		if delta < minDelta {
			minDelta = delta
		}
		if delta > maxDelta {
			maxDelta = delta
		}
	}
	if maxDelta-minDelta > tolerance {
		return fmt.Errorf("payload deltas vary by %d bytes, tolerance is %d",
			maxDelta-minDelta, tolerance)
	}
	return nil
}

func TestBoundedPayloadDeltaVariationOracle(t *testing.T) {
	const payloadSize = 320 << 10
	require.NoError(t, validateBoundedPayloadDeltaVariation(
		[]int{payloadSize - 4, payloadSize + 7, payloadSize}, 16))
	require.EqualError(t, validateBoundedPayloadDeltaVariation(
		[]int{payloadSize, 2 * payloadSize, 3 * payloadSize}, payloadSize/64),
		"payload deltas vary by 655360 bytes, tolerance is 5120")
}

type checkpointWidthSample struct {
	children int
	bytes    int
}

func validateCheckpointWidthLinearity(samples []checkpointWidthSample, payloadBytes,
	fixedOverheadAllowance, absoluteVariationAllowance, relativeVariationPercent int) ([]int, error) {
	if len(samples) < 3 || payloadBytes <= 0 || fixedOverheadAllowance < 0 ||
		absoluteVariationAllowance < 0 || relativeVariationPercent < 0 {
		return nil, errors.New("width-growth oracle requires at least three samples and non-negative allowances")
	}
	perChildDeltas := make([]int, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		addedChildren := samples[i].children - samples[i-1].children
		addedBytes := samples[i].bytes - samples[i-1].bytes
		if addedChildren <= 0 || addedBytes <= 0 {
			return nil, fmt.Errorf("width and size must increase at sample %d", i)
		}
		perChildDelta := addedBytes / addedChildren
		if perChildDelta < payloadBytes {
			return nil, fmt.Errorf("sample %d adds %d bytes per child, less than payload size %d",
				i, perChildDelta, payloadBytes)
		}
		if perChildDelta > payloadBytes+fixedOverheadAllowance {
			return nil, fmt.Errorf("sample %d adds %d bytes per child, payload plus fixed overhead allowance is %d",
				i, perChildDelta, payloadBytes+fixedOverheadAllowance)
		}
		perChildDeltas = append(perChildDeltas, perChildDelta)
	}

	minDelta, maxDelta := perChildDeltas[0], perChildDeltas[0]
	for _, delta := range perChildDeltas[1:] {
		if delta < minDelta {
			minDelta = delta
		}
		if delta > maxDelta {
			maxDelta = delta
		}
	}
	allowedVariation := absoluteVariationAllowance +
		minDelta*relativeVariationPercent/100
	if maxDelta-minDelta > allowedVariation {
		return perChildDeltas, fmt.Errorf(
			"normalized per-child deltas vary by %d bytes, allowance is %d absolute plus %d%% relative (%d total)",
			maxDelta-minDelta, absoluteVariationAllowance, relativeVariationPercent, allowedVariation)
	}
	return perChildDeltas, nil
}

func TestCheckpointWidthLinearityOracle(t *testing.T) {
	linear := []checkpointWidthSample{
		{children: 1, bytes: 62_000},
		{children: 2, bytes: 74_100},
		{children: 3, bytes: 85_950},
		{children: 5, bytes: 110_000},
		{children: 6, bytes: 122_040},
	}
	_, err := validateCheckpointWidthLinearity(linear, 10_000, 3_000, 256, 2)
	require.NoError(t, err)

	quadratic := []checkpointWidthSample{
		{children: 1, bytes: 60_500},
		{children: 2, bytes: 72_000},
		{children: 3, bytes: 84_500},
		{children: 5, bytes: 112_500},
		{children: 6, bytes: 128_000},
	}
	_, err = validateCheckpointWidthLinearity(quadratic, 10_000, 10_000, 256, 2)
	require.EqualError(t, err,
		"normalized per-child deltas vary by 4000 bytes, allowance is 256 absolute plus 2% relative (486 total)")
}

func checkpointUserFacingAddress(address Address) string {
	filtered := make(Address, 0, len(address))
	for _, segment := range address {
		if segment.Type == AddressSegmentAgent || segment.Type == AddressSegmentTool {
			filtered = append(filtered, segment)
		}
	}
	return filtered.String()
}

type checkpointWidthChild struct {
	agentName string
	toolName  string
	payload   string
}

func checkpointPathContainsContext(path []string, interruptCtx *InterruptCtx) bool {
	joined := strings.Join(path, "\x00")
	for current := interruptCtx; current != nil; current = current.Parent {
		if strings.Contains(joined, "@interrupt:"+current.ID) ||
			strings.Contains(joined, "@runner:"+current.ID) {
			return true
		}
	}
	return false
}

func newCheckpointWidthAgent(t *testing.T, width, payloadSize int) (Agent, []checkpointWidthChild) {
	t.Helper()
	children := make([]checkpointWidthChild, width)
	tools := make([]componenttool.BaseTool, 0, width)
	names := make([]string, 0, width)
	for i := 0; i < width; i++ {
		agentName := fmt.Sprintf("WidthChild%02d", i)
		toolName := agentName + "Interrupt"
		prefix := fmt.Sprintf("width-child-%02d:", i)
		require.GreaterOrEqual(t, payloadSize, len(prefix))
		payload := prefix + strings.Repeat(string(rune('a'+i)), payloadSize-len(prefix))
		children[i] = checkpointWidthChild{
			agentName: agentName,
			toolName:  toolName,
			payload:   payload,
		}
		child := newCheckpointCompatChatModelAgent(t, agentName, []string{toolName},
			[]componenttool.BaseTool{&checkpointCompatInterruptTool{name: toolName}},
			"content", payload)
		tools = append(tools, NewAgentTool(context.Background(), child))
		names = append(names, agentName)
	}
	return newCheckpointCompatChatModelAgent(t, "WidthParent", names, tools, "", ""), children
}

func captureCheckpointWidthFixture(t *testing.T, name string, width,
	payloadSize int) ([]byte, *InterruptInfo, []checkpointWidthChild) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	agent, children := newCheckpointWidthAgent(t, width, payloadSize)
	store := newCheckpointCompatStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		CheckPointStore: store,
	})
	iter := runner.Query(ctx, "start", WithCheckPointID(name))
	var original *InterruptInfo
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			original = event.Action.Interrupted
		}
	}
	require.NoError(t, ctx.Err())
	require.NotNil(t, original)
	require.Len(t, original.InterruptContexts, width)
	raw, exists, err := store.Get(ctx, name)
	require.NoError(t, err)
	require.True(t, exists)
	return raw, original, children
}

func requireCheckpointWidthRestoration(t *testing.T, raw []byte, original *InterruptInfo,
	children []checkpointWidthChild) {
	t.Helper()
	var restored serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&restored))
	index, err := restoreRunnerCheckpointProjectionWithTraversal(
		&restored, newCheckpointProjectionTraversal())
	require.NoError(t, err)
	require.NotNil(t, index)
	require.NotNil(t, restored.Info)

	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), "width-restoration", raw))
	_, _, resumeInfo, err := runnerLoadCheckPointImpl(
		store, context.Background(), "width-restoration")
	require.NoError(t, err)
	require.NotNil(t, resumeInfo)
	require.NotNil(t, resumeInfo.InterruptInfo)
	require.Equal(t, original.Data, resumeInfo.InterruptInfo.Data)
	originalChatModelInfo, ok := original.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.NotNil(t, originalChatModelInfo)
	require.NotNil(t, originalChatModelInfo.Info)
	restoredChatModelInfo, ok := resumeInfo.InterruptInfo.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.NotNil(t, restoredChatModelInfo)
	require.NotNil(t, restoredChatModelInfo.Info)
	require.Len(t, restoredChatModelInfo.Info.InterruptContexts, len(children))

	messages := index.allMessages()
	for i, child := range children {
		wantContext := originalChatModelInfo.Info.InterruptContexts[i]
		gotContext := restoredChatModelInfo.Info.InterruptContexts[i]
		require.Equal(t, wantContext.ID, gotContext.ID)
		require.True(t, wantContext.EqualsWithoutID(gotContext))
		require.Equal(t, fmt.Sprintf(
			"agent:WidthParent;tool:%s:call-%d;agent:%s;tool:%s:call-0",
			child.agentName, i, child.agentName, child.toolName),
			checkpointUserFacingAddress(gotContext.Address))

		var matches []canonicalCheckpointMessage
		for _, candidate := range messages {
			if candidate.message != nil && candidate.message.Content == child.payload {
				matches = append(matches, candidate)
			}
		}
		require.Len(t, matches, 1)
		match := matches[0]
		require.Equal(t, 1, match.source.AgentToolDepth)
		require.True(t, checkpointPathContainsContext(match.source.GraphPath, wantContext),
			"payload for %s must remain in its child checkpoint path %v",
			child.agentName, match.source.GraphPath)
		wantMessage := (&checkpointCompatModel{
			toolNames:    []string{child.toolName},
			payload:      child.payload,
			payloadField: "content",
		}).response(nil)
		typedSetMessageID(wantMessage, match.source.MessageID)
		require.Equal(t, wantMessage, match.message)
		require.Len(t, []byte(match.message.Content), len([]byte(child.payload)))
		require.Equal(t, []byte(child.payload), []byte(match.message.Content))
	}
}

func composeMessageProjectionCount(t *testing.T, data []byte) int {
	t.Helper()
	count := 0
	require.NoError(t, compose.WalkCheckpointValues(data, &gobSerializer{},
		func(_ compose.NodePath, _ compose.CheckpointValueLocation, value any) error {
			switch value.(type) {
			case *checkpointMessagePlaceholderV1,
				*checkpointMessageSlicePlaceholderV1,
				*checkpointAgenticMessagePlaceholderV1,
				*checkpointAgenticMessageSlicePlaceholderV1:
				count++
			}
			return nil
		}))
	return count
}

func captureMapBearingCancelCheckpoint(t *testing.T, name string, streaming bool,
	payloadSize int) []byte {
	t.Helper()
	ctx := context.Background()
	const toolName = "CancelCompletionTool"
	modelOutput := toolCallMsg(toolCall("cancel-call", toolName, `{}`))
	modelOutput.Extra = map[string]any{
		"checkpoint_payload": strings.Repeat("x", payloadSize),
		"map_a":              "a",
		"map_b":              "b",
		"map_c":              "c",
		"map_d":              "d",
	}
	blockingModel := newBlockingChatModel(modelOutput)
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "MapBearingCancelAgent",
		Description: "map-bearing checkpoint projection test agent",
		Model:       blockingModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []componenttool.BaseTool{
					&checkpointCompatCompletionTool{name: toolName},
				},
			},
		},
	})
	require.NoError(t, err)
	store := newCheckpointCompatStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: streaming,
		CheckPointStore: store,
	})
	cancelOpt, cancelFn := WithCancel()
	cancelCtx := getCommonOptions(nil, cancelOpt).cancelCtx
	iter := runner.Query(ctx, "start", WithCheckPointID(name), cancelOpt)

	select {
	case <-blockingModel.started:
	case <-time.After(5 * time.Second):
		t.Fatal("map-bearing checkpoint model did not start")
	}
	done := make(chan error, 1)
	go func() {
		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		done <- handle.Wait()
	}()
	select {
	case <-cancelCtx.cancelChan:
	case <-time.After(5 * time.Second):
		t.Fatal("map-bearing checkpoint cancel request was not registered")
	}
	close(blockingModel.unblockCh)
	select {
	case err = <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("map-bearing checkpoint cancel request did not complete")
	}

	cancelEvents := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err == nil {
			continue
		}
		var cancelErr *CancelError
		require.ErrorAs(t, event.Err, &cancelErr)
		cancelEvents++
	}
	require.Equal(t, 1, cancelEvents)
	raw, exists, err := store.Get(ctx, name)
	require.NoError(t, err)
	require.True(t, exists)
	return raw
}

func TestCheckpointSizeBreakdown(t *testing.T) {
	tests := []checkpointCompatFixture{
		{Name: "single_invoke", PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "single_stream", Streaming: true, PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "agent_tool_invoke", Depth: 1, PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "agent_tool_stream", Depth: 1, Streaming: true, PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "agent_tool_320k", Depth: 1, PayloadField: "content", PayloadSize: 320 << 10},
		{Name: "parallel_6", ParallelChildren: 6, PayloadField: "content", PayloadSize: 32 << 10},
	}
	for _, spec := range tests {
		t.Run(spec.Name, func(t *testing.T) {
			raw, _, _ := captureCheckpointCompatFixture(t, spec)
			breakdown := measureCheckpointSizeBreakdown(t, raw)
			t.Logf("checkpoint size breakdown: %+v", breakdown)
			require.Equal(t, len(raw), breakdown.RunnerRawBytes)
			require.Positive(t, breakdown.RunCtxBytes)
			require.Positive(t, breakdown.LegacyInfoBytes)
			require.Positive(t, breakdown.OuterInterruptStateBytes)
			require.Positive(t, breakdown.ComposeStateBytes)
			require.Positive(t, breakdown.ComposeInterruptStateBytes)
			if spec.Depth > 0 || spec.ParallelChildren > 0 {
				require.Positive(t, breakdown.AgentToolChildRunnerBytes)
			}
		})
	}
}

func TestCheckpointSizeScalesLinearlyWithDepthAndWidth(t *testing.T) {
	const (
		payloadSize  = 320 << 10
		shallowDepth = 1
		maxDepth     = 8
	)

	structuralSizes := make([]int, maxDepth+1)
	payloadDeltas := make([]int, maxDepth+1)
	payloadCheckpoints := make([][]byte, maxDepth+1)
	interruptIDsByDepth := make([][]string, maxDepth+1)
	interruptAddressesByDepth := make([][]string, maxDepth+1)
	t.Run("depth_measurements", func(t *testing.T) {
		const maxConcurrentCaptures = 3
		captureSlots := make(chan struct{}, maxConcurrentCaptures)
		for depth := 0; depth <= maxDepth; depth++ {
			depth := depth
			t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
				t.Parallel()
				captureSlots <- struct{}{}
				defer func() { <-captureSlots }()

				baseSpec := checkpointCompatFixture{
					Name:             fmt.Sprintf("depth-invoke-%d-base", depth),
					Depth:            depth,
					PayloadField:     "content",
					StableDepthNames: true,
				}
				baseRaw, _, _ := captureCheckpointCompatFixture(t, baseSpec)
				structuralSizes[depth] = len(baseRaw)

				payloadSpec := baseSpec
				payloadSpec.Name = fmt.Sprintf("depth-invoke-%d-payload", depth)
				payloadSpec.PayloadSize = payloadSize
				payloadRaw, interruptIDs, interruptAddresses :=
					captureCheckpointCompatFixture(t, payloadSpec)
				payloadCheckpoints[depth] = payloadRaw
				interruptIDsByDepth[depth] = interruptIDs
				interruptAddressesByDepth[depth] = interruptAddresses
				payloadDelta := len(payloadRaw) - len(baseRaw)
				payloadDeltas[depth] = payloadDelta
				t.Logf("depth=%d structural=%d logical_payload=%d encoded_payload_delta=%d",
					depth, len(baseRaw), payloadSize, payloadDelta)

				require.GreaterOrEqual(t, payloadDelta, payloadSize,
					"checkpoint growth must retain the selected payload")
				matches := projectedCheckpointPayloadMessages(
					t, payloadRaw, payloadSpec.PayloadField, payloadSpec.PayloadSize)
				require.Len(t, matches, 1)
				require.Equal(t, depth, matches[0].source.AgentToolDepth)

				if depth == shallowDepth {
					requireCheckpointSemanticRestoreAndResume(
						t, payloadRaw, payloadSpec, interruptIDs, interruptAddresses)
				}
			})
		}
	})

	increments := make([]int, maxDepth)
	for depth := 1; depth <= maxDepth; depth++ {
		increments[depth-1] = structuralSizes[depth] - structuralSizes[depth-1]
		require.Greater(t, increments[depth-1], 1<<10,
			"an added AgentTool layer must retain its nested child state")
	}
	require.NoError(t, validateConstantStructuralGrowth(structuralSizes, 256),
		"structural growth must remain constant across depth 0-8: increments=%v",
		increments)
	require.NoError(t, validateBoundedPayloadDeltaVariation(payloadDeltas, 1<<10),
		"large-payload delta must remain stable across depth 0-8: deltas=%v",
		payloadDeltas)

	deepestSpec := checkpointCompatFixture{
		Name:             fmt.Sprintf("depth-invoke-%d-payload", maxDepth),
		Depth:            maxDepth,
		Streaming:        true,
		PayloadField:     "content",
		PayloadSize:      payloadSize,
		StableDepthNames: true,
	}
	requireCheckpointSemanticRestoreAndResume(
		t, payloadCheckpoints[maxDepth], deepestSpec,
		interruptIDsByDepth[maxDepth], interruptAddressesByDepth[maxDepth])

	const (
		widthFixedOverheadAllowance     = 64 << 10
		widthAbsoluteVariationAllowance = 8 << 10
		widthRelativeVariationPercent   = 2
	)
	widths := []int{1, 2, 3, 4, 6}
	widthSamples := make([]checkpointWidthSample, len(widths))
	for i, width := range widths {
		raw, original, children := captureCheckpointWidthFixture(
			t, fmt.Sprintf("width-%d", width), width, payloadSize)
		widthSamples[i] = checkpointWidthSample{children: width, bytes: len(raw)}
		t.Logf("width=%d checkpoint bytes=%d", width, len(raw))
		requireCheckpointWidthRestoration(t, raw, original, children)
	}
	perChildDeltas, err := validateCheckpointWidthLinearity(
		widthSamples, payloadSize, widthFixedOverheadAllowance,
		widthAbsoluteVariationAllowance, widthRelativeVariationPercent)
	require.NoError(t, err,
		"checkpoint width growth must have stable normalized per-child deltas: samples=%v deltas=%v",
		widthSamples, perChildDeltas)
	t.Logf("width samples=%v normalized per-child deltas=%v", widthSamples, perChildDeltas)
}

func TestCheckpointMapBearingProjectionIsStable(t *testing.T) {
	const (
		iterations  = 20
		payloadSize = 320 << 10
	)
	for _, streaming := range []bool{false, true} {
		mode := "invoke"
		if streaming {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			for iteration := 0; iteration < iterations; iteration++ {
				raw := captureMapBearingCancelCheckpoint(t,
					fmt.Sprintf("map-projection-%s-%d", mode, iteration),
					streaming, payloadSize)
				var persisted serialization
				require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&persisted))
				require.NotNil(t, persisted.ProjectionV1,
					"iteration %d must retain runner projection", iteration)

				sourceID := persisted.ProjectionV1.SourceInterruptID
				sourceState, exists := persisted.InterruptID2State[sourceID]
				require.True(t, exists)
				sourceData, ok := sourceState.State.([]byte)
				require.True(t, ok)
				require.Positive(t, composeMessageProjectionCount(t, sourceData),
					"iteration %d must retain Compose value projection", iteration)

				require.NoError(t, restoreRunnerCheckpointProjection(&persisted))
				persisted.ProjectionV1 = nil
				expanded, err := encodeRunnerCheckpoint(&persisted)
				require.NoError(t, err)
				require.Greater(t, len(expanded)-len(raw), payloadSize/2,
					"iteration %d must retain the projection size benefit", iteration)
				require.Len(t, restoredCheckpointPayloadMessages(
					t, raw, "extra", payloadSize), 1)
			}
		})
	}
}

func TestCheckpointSizeMatrix(t *testing.T) {
	t.Run("small_payload", func(t *testing.T) {
		single, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
			Name: "small-single", PayloadField: "content",
		})
		nested, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
			Name: "small-nested", Depth: 1, PayloadField: "content",
		})
		require.Less(t, len(single), 30_000)
		require.Less(t, len(nested), 120_000)
	})

	sizes := []int{0, 256 << 10, 320 << 10, 1 << 20}
	for _, field := range []string{
		"user_query",
		"content",
		"reasoning",
		"tool_arguments",
		"extra",
		"multimodal",
	} {
		baseSizes := make([]int, 2)
		for _, size := range sizes {
			modeSizes := make([]int, 2)
			for mode, streaming := range []bool{false, true} {
				name := fmt.Sprintf("%s_size_%d_stream_%t", field, size, streaming)
				t.Run(name, func(t *testing.T) {
					raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
						Name:         name,
						Depth:        1,
						Streaming:    streaming,
						PayloadField: field,
						PayloadSize:  size,
					})
					t.Logf("field=%s payload=%d streaming=%t checkpoint=%d",
						field, size, streaming, len(raw))
					multiplier := 1
					if field == "user_query" {
						multiplier = 4
					}
					require.Less(t, len(raw), multiplier*size+(128<<10),
						"checkpoint size must grow linearly with the selected payload")
					if size == 0 {
						baseSizes[mode] = len(raw)
					} else {
						require.GreaterOrEqual(t, len(raw)-baseSizes[mode], size*99/100,
							"checkpoint growth must retain the selected payload")
						require.Len(t,
							restoredCheckpointPayloadMessages(t, raw, field, size),
							1,
							"restored checkpoint must contain the selected payload")
					}
					modeSizes[mode] = len(raw)
				})
			}
			delta := modeSizes[0] - modeSizes[1]
			if delta < 0 {
				delta = -delta
			}
			require.Less(t, delta, 1<<10,
				"Invoke and Stream checkpoints must have equivalent size behavior")
		}
	}
}
