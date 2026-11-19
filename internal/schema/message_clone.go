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

package schema

import (
	"github.com/cloudwego/eino/internal/gmap"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/schema/claude"
	"github.com/cloudwego/eino/schema/gemini"
	"github.com/cloudwego/eino/schema/openai"
)

// CloneMessage copies a message's known structs, pointers, maps and slices,
// preserving nil values and runtime-only fields. It does not validate or serialize.
//
// Extra maps are copied; their keys and values are assigned as-is. Opaque any
// payloads are also assigned as-is, retaining any references they contain.
// ToolInfo and its Extra map are copied; ParamsOneOf and JSON schemas are shared
// as read-only parameter definitions.
//
// Callers must prevent concurrent mutation of the source during cloning and
// keep shared data read-only after publication. CloneMessage does not acquire
// application-owned locks.
func CloneMessage[M *schema.Message | *schema.AgenticMessage](message M) M {
	switch v := any(message).(type) {
	case *schema.Message:
		return any(clonePointerWith(v, cloneMessage)).(M)
	case *schema.AgenticMessage:
		return any(clonePointerWith(v, cloneAgenticMessage)).(M)
	default:
		panic("unreachable: unknown message type")
	}
}

// CloneMessages copies the slice and each message using CloneMessage's contract.
func CloneMessages[M *schema.Message | *schema.AgenticMessage](messages []M) []M {
	return cloneSliceWith(messages, CloneMessage[M])
}

func cloneMessage(v schema.Message) schema.Message {
	v.MultiContent = cloneSliceWith(v.MultiContent, cloneChatMessagePart)
	v.UserInputMultiContent = cloneSliceWith(v.UserInputMultiContent, cloneMessageInputPart)
	v.AssistantGenMultiContent = cloneSliceWith(v.AssistantGenMultiContent, cloneMessageOutputPart)
	v.ToolCalls = cloneSliceWith(v.ToolCalls, cloneToolCall)
	v.ResponseMeta = clonePointerWith(v.ResponseMeta, cloneResponseMeta)
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneChatMessagePart(v schema.ChatMessagePart) schema.ChatMessagePart {
	v.ImageURL = clonePointerWith(v.ImageURL, cloneChatMessageImageURL)
	v.AudioURL = clonePointerWith(v.AudioURL, cloneChatMessageAudioURL)
	v.VideoURL = clonePointerWith(v.VideoURL, cloneChatMessageVideoURL)
	v.FileURL = clonePointerWith(v.FileURL, cloneChatMessageFileURL)
	return v
}

func cloneChatMessageImageURL(v schema.ChatMessageImageURL) schema.ChatMessageImageURL {
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneChatMessageAudioURL(v schema.ChatMessageAudioURL) schema.ChatMessageAudioURL {
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneChatMessageVideoURL(v schema.ChatMessageVideoURL) schema.ChatMessageVideoURL {
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneChatMessageFileURL(v schema.ChatMessageFileURL) schema.ChatMessageFileURL {
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneMessageInputPart(v schema.MessageInputPart) schema.MessageInputPart {
	v.Image = clonePointerWith(v.Image, cloneMessageInputImage)
	v.Audio = clonePointerWith(v.Audio, cloneMessageInputAudio)
	v.Video = clonePointerWith(v.Video, cloneMessageInputVideo)
	v.File = clonePointerWith(v.File, cloneMessageInputFile)
	v.ToolSearchResult = clonePointerWith(v.ToolSearchResult, cloneToolSearchResult)
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneMessageInputImage(v schema.MessageInputImage) schema.MessageInputImage {
	v.MessagePartCommon = cloneMessagePartCommon(v.MessagePartCommon)
	return v
}

func cloneMessagePartCommon(v schema.MessagePartCommon) schema.MessagePartCommon {
	v.URL = clonePointer(v.URL)
	v.Base64Data = clonePointer(v.Base64Data)
	v.FileID = clonePointer(v.FileID)
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneMessageInputAudio(v schema.MessageInputAudio) schema.MessageInputAudio {
	v.MessagePartCommon = cloneMessagePartCommon(v.MessagePartCommon)
	return v
}

func cloneMessageInputVideo(v schema.MessageInputVideo) schema.MessageInputVideo {
	v.MessagePartCommon = cloneMessagePartCommon(v.MessagePartCommon)
	return v
}

func cloneMessageInputFile(v schema.MessageInputFile) schema.MessageInputFile {
	v.MessagePartCommon = cloneMessagePartCommon(v.MessagePartCommon)
	return v
}

func cloneToolSearchResult(v schema.ToolSearchResult) schema.ToolSearchResult {
	v.Tools = clonePointerSlice(v.Tools, cloneToolInfo)
	return v
}

func cloneToolInfo(v schema.ToolInfo) schema.ToolInfo {
	// Extra gets an independent map with shared values. ParamsOneOf and its
	// nested ParameterInfo/JSONSchema data remain shared and read-only.
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneMessageOutputPart(v schema.MessageOutputPart) schema.MessageOutputPart {
	v.Image = clonePointerWith(v.Image, cloneMessageOutputImage)
	v.Audio = clonePointerWith(v.Audio, cloneMessageOutputAudio)
	v.Video = clonePointerWith(v.Video, cloneMessageOutputVideo)
	v.Reasoning = clonePointer(v.Reasoning)
	v.Extra = gmap.Clone(v.Extra)
	v.StreamingMeta = clonePointer(v.StreamingMeta)
	return v
}

func cloneMessageOutputImage(v schema.MessageOutputImage) schema.MessageOutputImage {
	v.MessagePartCommon = cloneMessagePartCommon(v.MessagePartCommon)
	return v
}

func cloneMessageOutputAudio(v schema.MessageOutputAudio) schema.MessageOutputAudio {
	v.MessagePartCommon = cloneMessagePartCommon(v.MessagePartCommon)
	return v
}

func cloneMessageOutputVideo(v schema.MessageOutputVideo) schema.MessageOutputVideo {
	v.MessagePartCommon = cloneMessagePartCommon(v.MessagePartCommon)
	return v
}

func cloneToolCall(v schema.ToolCall) schema.ToolCall {
	v.Index = clonePointer(v.Index)
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneResponseMeta(v schema.ResponseMeta) schema.ResponseMeta {
	v.Usage = clonePointer(v.Usage)
	v.LogProbs = clonePointerWith(v.LogProbs, cloneLogProbs)
	return v
}

func cloneLogProbs(v schema.LogProbs) schema.LogProbs {
	v.Content = cloneSliceWith(v.Content, cloneLogProb)
	return v
}

func cloneLogProb(v schema.LogProb) schema.LogProb {
	v.Bytes = cloneSlice(v.Bytes)
	v.TopLogProbs = cloneSliceWith(v.TopLogProbs, cloneTopLogProb)
	return v
}

func cloneTopLogProb(v schema.TopLogProb) schema.TopLogProb {
	v.Bytes = cloneSlice(v.Bytes)
	return v
}

func cloneAgenticMessage(v schema.AgenticMessage) schema.AgenticMessage {
	v.ContentBlocks = clonePointerSlice(v.ContentBlocks, cloneContentBlock)
	v.ResponseMeta = clonePointerWith(v.ResponseMeta, cloneAgenticResponseMeta)
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneContentBlock(v schema.ContentBlock) schema.ContentBlock {
	v.Reasoning = clonePointerWith(v.Reasoning, cloneReasoning)
	v.UserInputText = clonePointer(v.UserInputText)
	v.UserInputImage = clonePointer(v.UserInputImage)
	v.UserInputAudio = clonePointer(v.UserInputAudio)
	v.UserInputVideo = clonePointer(v.UserInputVideo)
	v.UserInputFile = clonePointer(v.UserInputFile)
	v.AssistantGenText = clonePointerWith(v.AssistantGenText, cloneAssistantGenText)
	v.AssistantGenImage = clonePointer(v.AssistantGenImage)
	v.AssistantGenAudio = clonePointer(v.AssistantGenAudio)
	v.AssistantGenVideo = clonePointer(v.AssistantGenVideo)
	v.FunctionToolCall = clonePointer(v.FunctionToolCall)
	v.FunctionToolResult = clonePointerWith(v.FunctionToolResult, cloneFunctionToolResult)
	v.ToolSearchFunctionToolResult = clonePointerWith(v.ToolSearchFunctionToolResult, cloneToolSearchFunctionToolResult)
	// The wrappers are copied; their opaque Arguments and Content values
	// remain shared under CloneMessage's read-only publication contract.
	v.ServerToolCall = clonePointer(v.ServerToolCall)
	v.ServerToolResult = clonePointer(v.ServerToolResult)
	v.MCPToolCall = clonePointer(v.MCPToolCall)
	v.MCPToolResult = clonePointerWith(v.MCPToolResult, cloneMCPToolResult)
	v.MCPListToolsResult = clonePointerWith(v.MCPListToolsResult, cloneMCPListToolsResult)
	v.MCPToolApprovalRequest = clonePointer(v.MCPToolApprovalRequest)
	v.MCPToolApprovalResponse = clonePointer(v.MCPToolApprovalResponse)
	v.StreamingMeta = clonePointer(v.StreamingMeta)
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneReasoning(v schema.Reasoning) schema.Reasoning {
	v.OpenAIExtension = clonePointerWith(v.OpenAIExtension, cloneOpenaiReasoningExtension)
	return v
}

func cloneOpenaiReasoningExtension(v openai.ReasoningExtension) openai.ReasoningExtension {
	v.Content = clonePointerSlice(v.Content, cloneOpenaiReasoningContent)
	return v
}

func cloneOpenaiReasoningContent(v openai.ReasoningContent) openai.ReasoningContent {
	v.Index = clonePointer(v.Index)
	return v
}

func cloneAssistantGenText(v schema.AssistantGenText) schema.AssistantGenText {
	// Typed provider extensions are copied; the opaque Extension value
	// remains shared under CloneMessage's read-only publication contract.
	v.OpenAIExtension = clonePointerWith(v.OpenAIExtension, cloneOpenaiAssistantGenTextExtension)
	v.ClaudeExtension = clonePointerWith(v.ClaudeExtension, cloneClaudeAssistantGenTextExtension)
	return v
}

func cloneOpenaiAssistantGenTextExtension(v openai.AssistantGenTextExtension) openai.AssistantGenTextExtension {
	v.Refusal = clonePointer(v.Refusal)
	v.Annotations = clonePointerSlice(v.Annotations, cloneOpenaiTextAnnotation)
	return v
}

func cloneOpenaiTextAnnotation(v openai.TextAnnotation) openai.TextAnnotation {
	v.FileCitation = clonePointer(v.FileCitation)
	v.URLCitation = clonePointer(v.URLCitation)
	v.ContainerFileCitation = clonePointer(v.ContainerFileCitation)
	v.FilePath = clonePointer(v.FilePath)
	return v
}

func cloneClaudeAssistantGenTextExtension(v claude.AssistantGenTextExtension) claude.AssistantGenTextExtension {
	v.Citations = clonePointerSlice(v.Citations, cloneClaudeTextCitation)
	return v
}

func cloneClaudeTextCitation(v claude.TextCitation) claude.TextCitation {
	v.CharLocation = clonePointer(v.CharLocation)
	v.PageLocation = clonePointer(v.PageLocation)
	v.ContentBlockLocation = clonePointer(v.ContentBlockLocation)
	v.WebSearchResultLocation = clonePointer(v.WebSearchResultLocation)
	return v
}

func cloneFunctionToolResult(v schema.FunctionToolResult) schema.FunctionToolResult {
	v.Content = clonePointerSlice(v.Content, cloneFunctionToolResultContentBlock)
	return v
}

func cloneFunctionToolResultContentBlock(v schema.FunctionToolResultContentBlock) schema.FunctionToolResultContentBlock {
	v.Text = clonePointer(v.Text)
	v.Image = clonePointer(v.Image)
	v.Audio = clonePointer(v.Audio)
	v.Video = clonePointer(v.Video)
	v.File = clonePointer(v.File)
	v.Extra = gmap.Clone(v.Extra)
	return v
}

func cloneToolSearchFunctionToolResult(v schema.ToolSearchFunctionToolResult) schema.ToolSearchFunctionToolResult {
	v.Result = clonePointerWith(v.Result, cloneToolSearchResult)
	return v
}

func cloneMCPToolResult(v schema.MCPToolResult) schema.MCPToolResult {
	v.Error = clonePointerWith(v.Error, cloneMCPToolCallError)
	return v
}

func cloneMCPToolCallError(v schema.MCPToolCallError) schema.MCPToolCallError {
	v.Code = clonePointer(v.Code)
	return v
}

func cloneMCPListToolsResult(v schema.MCPListToolsResult) schema.MCPListToolsResult {
	// The list and item structs are copied; each InputSchema remains shared
	// as a read-only parameter definition.
	v.Tools = cloneSliceWith(v.Tools, clonePointer[schema.MCPListToolsItem])
	return v
}

func cloneAgenticResponseMeta(v schema.AgenticResponseMeta) schema.AgenticResponseMeta {
	// Usage and typed provider extensions are copied; the opaque Extension
	// value remains shared under CloneMessage's read-only publication contract.
	v.TokenUsage = clonePointer(v.TokenUsage)
	v.OpenAIExtension = clonePointerWith(v.OpenAIExtension, cloneOpenaiResponseMetaExtension)
	v.GeminiExtension = clonePointerWith(v.GeminiExtension, cloneGeminiResponseMetaExtension)
	v.ClaudeExtension = clonePointerWith(v.ClaudeExtension, cloneClaudeResponseMetaExtension)
	return v
}

func cloneOpenaiResponseMetaExtension(v openai.ResponseMetaExtension) openai.ResponseMetaExtension {
	v.Error = clonePointer(v.Error)
	v.IncompleteDetails = clonePointer(v.IncompleteDetails)
	v.Reasoning = clonePointer(v.Reasoning)
	return v
}

func cloneGeminiResponseMetaExtension(v gemini.ResponseMetaExtension) gemini.ResponseMetaExtension {
	v.GroundingMeta = clonePointerWith(v.GroundingMeta, cloneGeminiGroundingMetadata)
	return v
}

func cloneGeminiGroundingMetadata(v gemini.GroundingMetadata) gemini.GroundingMetadata {
	v.GroundingChunks = clonePointerSlice(v.GroundingChunks, cloneGeminiGroundingChunk)
	v.GroundingSupports = clonePointerSlice(v.GroundingSupports, cloneGeminiGroundingSupport)
	v.SearchEntryPoint = clonePointerWith(v.SearchEntryPoint, cloneGeminiSearchEntryPoint)
	v.WebSearchQueries = cloneSlice(v.WebSearchQueries)
	return v
}

func cloneGeminiGroundingChunk(v gemini.GroundingChunk) gemini.GroundingChunk {
	v.Web = clonePointer(v.Web)
	return v
}

func cloneGeminiGroundingSupport(v gemini.GroundingSupport) gemini.GroundingSupport {
	v.ConfidenceScores = cloneSlice(v.ConfidenceScores)
	v.GroundingChunkIndices = cloneSlice(v.GroundingChunkIndices)
	v.Segment = clonePointer(v.Segment)
	return v
}

func cloneGeminiSearchEntryPoint(v gemini.SearchEntryPoint) gemini.SearchEntryPoint {
	v.SDKBlob = cloneSlice(v.SDKBlob)
	return v
}

func cloneClaudeResponseMetaExtension(v claude.ResponseMetaExtension) claude.ResponseMetaExtension {
	v.StopDetails = clonePointer(v.StopDetails)
	return v
}

// clonePointer returns a shallow copy of a framework-owned value, preserving nil.
// Reference fields remain shared unless the caller copies them separately.
func clonePointer[T any](v *T) *T {
	if v == nil {
		return nil
	}
	copied := *v
	return &copied
}

func clonePointerWith[T any](v *T, clone func(T) T) *T {
	if v == nil {
		return nil
	}
	copied := clone(*v)
	return &copied
}

// cloneSlice copies the backing array and preserves nil versus empty slices.
// Elements are assigned as-is, retaining any references they contain.
func cloneSlice[T any](v []T) []T {
	if v == nil {
		return nil
	}
	copied := make([]T, len(v))
	copy(copied, v)
	return copied
}

func cloneSliceWith[T any](v []T, clone func(T) T) []T {
	if v == nil {
		return nil
	}
	copied := make([]T, len(v))
	for i := range v {
		copied[i] = clone(v[i])
	}
	return copied
}

func clonePointerSlice[T any](v []*T, clone func(T) T) []*T {
	return cloneSliceWith(v, func(p *T) *T { return clonePointerWith(p, clone) })
}
