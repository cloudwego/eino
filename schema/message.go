/*
 * Copyright 2024 CloudWeGo Authors
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
	"context"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"text/template"

	"github.com/nikolalohinski/gonja"
	"github.com/nikolalohinski/gonja/config"
	"github.com/nikolalohinski/gonja/nodes"
	"github.com/nikolalohinski/gonja/parser"
	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/generic"
)

func init() {
	internal.RegisterStreamChunkConcatFunc(ConcatMessages)
	internal.RegisterStreamChunkConcatFunc(ConcatMessageArray)

	internal.RegisterStreamChunkConcatFunc(ConcatToolResults)
}

// ConcatMessageArray merges aligned slices of messages into a single slice,
// concatenating messages at the same index across the input arrays.
func ConcatMessageArray(mas [][]*Message) ([]*Message, error) {
	arrayLen := len(mas[0])

	ret := make([]*Message, arrayLen)
	slicesToConcat := make([][]*Message, arrayLen)

	for _, ma := range mas {
		if len(ma) != arrayLen {
			return nil, fmt.Errorf("unexpected array length. "+
				"Got %d, expected %d", len(ma), arrayLen)
		}

		for i := 0; i < arrayLen; i++ {
			m := ma[i]
			if m != nil {
				slicesToConcat[i] = append(slicesToConcat[i], m)
			}
		}
	}

	for i, slice := range slicesToConcat {
		if len(slice) == 0 {
			ret[i] = nil
		} else if len(slice) == 1 {
			ret[i] = slice[0]
		} else {
			cm, err := ConcatMessages(slice)
			if err != nil {
				return nil, err
			}

			ret[i] = cm
		}
	}

	return ret, nil
}

// FormatType used by MessageTemplate.Format
type FormatType uint8

const (
	// FString Supported by pyfmt(github.com/slongfield/pyfmt), which is an implementation of https://peps.python.org/pep-3101/.
	FString FormatType = 0
	// GoTemplate https://pkg.go.dev/text/template.
	GoTemplate FormatType = 1
	// Jinja2 Supported by gonja(github.com/nikolalohinski/gonja), which is a implementation of https://jinja.palletsprojects.com/en/3.1.x/templates/.
	Jinja2 FormatType = 2
)

// RoleType is the type of the role of a message.
type RoleType string

const (
	// Assistant is the role of an assistant, means the message is returned by ChatModel.
	Assistant RoleType = "assistant"
	// User is the role of a user, means the message is a user message.
	User RoleType = "user"
	// System is the role of a system, means the message is a system message.
	System RoleType = "system"
	// Tool is the role of a tool, means the message is a tool call output.
	Tool RoleType = "tool"
)

// FunctionCall is the function call in a message.
// It's used in Assistant Message.
type FunctionCall struct {
	// Name is the name of the function to call, it can be used to identify the specific function.
	Name string `json:"name,omitempty"`
	// Arguments is the arguments to call the function with, in JSON format.
	Arguments string `json:"arguments,omitempty"`
}

// ToolCall is the tool call in a message.
// It's used in Assistant Message when there are tool calls should be made.
type ToolCall struct {
	// Index is used when there are multiple tool calls in a message.
	// In stream mode, it's used to identify the chunk of the tool call for merging.
	Index *int `json:"index,omitempty"`
	// ID is the id of the tool call, it can be used to identify the specific tool call.
	ID string `json:"id"`
	// Type is the type of the tool call, default is "function".
	Type string `json:"type"`
	// Function is the function call to be made.
	Function FunctionCall `json:"function"`

	// Extra is used to store extra information for the tool call.
	Extra map[string]any `json:"extra,omitempty"`
}

// ImageURLDetail is the detail of the image url.
type ImageURLDetail string

const (
	// ImageURLDetailHigh means the high quality image url.
	ImageURLDetailHigh ImageURLDetail = "high"
	// ImageURLDetailLow means the low quality image url.
	ImageURLDetailLow ImageURLDetail = "low"
	// ImageURLDetailAuto means the auto quality image url.
	ImageURLDetailAuto ImageURLDetail = "auto"
)

// MessagePartCommon represents the common abstract components for input and output of multi-modal types.
type MessagePartCommon struct {
	// URL is primarily used for HTTP or HTTPS access links.
	// For data in the format 'data:[<mediatype>][;base64],<data>' (the 'data' URL Schema of RFC-2397 (https://www.rfc-editor.org/rfc/rfc2397)),
	// it is recommended to use Base64Data and MIMEType fields separately instead.
	URL *string `json:"url,omitempty"`

	// Base64Data represents the binary data in Base64 encoded string format.
	Base64Data *string `json:"base64data,omitempty"`

	// MIMEType is the mime type , eg."image/png",""audio/wav" etc.
	MIMEType string `json:"mime_type,omitempty"`

	// Extra is used to store extra information.
	Extra map[string]any `json:"extra,omitempty"`
}

// MessageInputImage is used to represent an image part in message.
// Choose either URL or Base64Data.
type MessageInputImage struct {
	MessagePartCommon

	// Detail is the quality of the image url.
	Detail ImageURLDetail `json:"detail,omitempty"`
}

// MessageInputAudio is used to represent an audio part in message.
// Choose either URL or Base64Data.
type MessageInputAudio struct {
	MessagePartCommon
}

// MessageInputVideo is used to represent a video part in message.
// Choose either URL or Base64Data.
type MessageInputVideo struct {
	MessagePartCommon
}

// MessageInputFile is used to represent a file part in message.
// Choose either URL or Base64Data.
type MessageInputFile struct {
	MessagePartCommon

	// Name represents the filename.
	// Optional.
	Name string `json:"name,omitempty"`
}

// MessageInputPart represents the input part of message.
type MessageInputPart struct {
	Type ChatMessagePartType `json:"type"`

	Text string `json:"text,omitempty"`

	// Image is the image input of the part, it's used when Type is "image_url".
	Image *MessageInputImage `json:"image,omitempty"`

	// Audio  is the audio input of the part, it's used when Type is "audio_url".
	Audio *MessageInputAudio `json:"audio,omitempty"`

	// Video is the video input of the part, it's used when Type is "video_url".
	Video *MessageInputVideo `json:"video,omitempty"`

	// File is the file input of the part, it's used when Type is "file_url".
	File *MessageInputFile `json:"file,omitempty"`

	// Extra is used to store extra information.
	Extra map[string]any `json:"extra,omitempty"`
}

// MessageOutputImage is used to represent an image part in message.
type MessageOutputImage struct {
	MessagePartCommon
}

// MessageOutputAudio is used to represent an audio part in message.
type MessageOutputAudio struct {
	MessagePartCommon
}

// MessageOutputVideo is used to represent a video part in message.
type MessageOutputVideo struct {
	MessagePartCommon
}

// MessageOutputPart represents a part of an assistant-generated message.
// It can contain text, or multimedia content like images, audio, or video.
type MessageOutputPart struct {
	// Type is the type of the part, eg. "text", "image_url", "audio_url", "video_url".
	Type ChatMessagePartType `json:"type"`

	// Text is the text of the part, it's used when Type is "text".
	Text string `json:"text,omitempty"`

	// Image is the image output of the part, used when Type is ChatMessagePartTypeImageURL.
	Image *MessageOutputImage `json:"image,omitempty"`

	// Audio is the audio output of the part, used when Type is ChatMessagePartTypeAudioURL.
	Audio *MessageOutputAudio `json:"audio,omitempty"`

	// Video is the video output of the part, used when Type is ChatMessagePartTypeVideoURL.
	Video *MessageOutputVideo `json:"video,omitempty"`

	// Extra is used to store extra information.
	Extra map[string]any `json:"extra,omitempty"`
}

// ToolPartType defines the type of content in a tool output part.
// It is used to distinguish between different types of multimodal content returned by tools.
type ToolPartType string

const (
	// ToolPartTypeText means the part is a text.
	ToolPartTypeText ToolPartType = "text"

	// ToolPartTypeImage means the part is an image url.
	ToolPartTypeImage ToolPartType = "image"

	// ToolPartTypeAudio means the part is an audio url.
	ToolPartTypeAudio ToolPartType = "audio"

	// ToolPartTypeVideo means the part is a video url.
	ToolPartTypeVideo ToolPartType = "video"

	// ToolPartTypeFile means the part is a file url.
	ToolPartTypeFile ToolPartType = "file"
)

// ToolOutputImage represents an image in tool output.
// It contains URL or Base64-encoded data along with MIME type information.
type ToolOutputImage struct {
	MessagePartCommon
}

// ToolOutputAudio represents an audio file in tool output.
// It contains URL or Base64-encoded data along with MIME type information.
type ToolOutputAudio struct {
	MessagePartCommon
}

// ToolOutputVideo represents a video file in tool output.
// It contains URL or Base64-encoded data along with MIME type information.
type ToolOutputVideo struct {
	MessagePartCommon
}

// ToolOutputFile represents a generic file in tool output.
// It contains URL or Base64-encoded data along with MIME type information.
type ToolOutputFile struct {
	MessagePartCommon
}

// ToolOutputPart represents a part of tool execution output.
// It supports streaming scenarios through the Index field for chunk merging.
type ToolOutputPart struct {

	// Type is the type of the part, e.g., "text", "image_url", "audio_url", "video_url".
	Type ToolPartType `json:"type"`

	// Text is the text content, used when Type is "text".
	Text string `json:"text,omitempty"`

	// Image is the image content, used when Type is ToolPartTypeImage.
	Image *ToolOutputImage `json:"image,omitempty"`

	// Audio is the audio content, used when Type is ToolPartTypeAudio.
	Audio *ToolOutputAudio `json:"audio,omitempty"`

	// Video is the video content, used when Type is ToolPartTypeVideo.
	Video *ToolOutputVideo `json:"video,omitempty"`

	// File is the file content, used when Type is ToolPartTypeFile.
	File *ToolOutputFile `json:"file,omitempty"`

	// Extra is used to store extra information.
	Extra map[string]any `json:"extra,omitempty"`
}

// ToolArgument contains the input information for a tool call.
// It is used to pass tool call arguments to enhanced tools.
type ToolArgument struct {
	// Text contains the arguments for the tool call in JSON format.
	Text string `json:"text,omitempty"`
}

// ToolResult represents the structured multimodal output from a tool execution.
// It is used when a tool needs to return more than just a simple string,
// such as images, files, or other structured data.
type ToolResult struct {
	// Parts contains the multimodal output parts. Each part can be a different
	// type of content, like text, an image, or a file.
	Parts []ToolOutputPart `json:"parts,omitempty"`
}

func convToolOutputPartToMessageInputPart(toolPart ToolOutputPart) (MessageInputPart, error) {
	switch toolPart.Type {
	case ToolPartTypeText:
		return MessageInputPart{
			Type: ChatMessagePartTypeText,
			Text: toolPart.Text,
		}, nil
	case ToolPartTypeImage:
		if toolPart.Image == nil {
			return MessageInputPart{}, fmt.Errorf("image content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:  ChatMessagePartTypeImageURL,
			Image: &MessageInputImage{MessagePartCommon: toolPart.Image.MessagePartCommon},
		}, nil
	case ToolPartTypeAudio:
		if toolPart.Audio == nil {
			return MessageInputPart{}, fmt.Errorf("audio content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:  ChatMessagePartTypeAudioURL,
			Audio: &MessageInputAudio{MessagePartCommon: toolPart.Audio.MessagePartCommon},
		}, nil
	case ToolPartTypeVideo:
		if toolPart.Video == nil {
			return MessageInputPart{}, fmt.Errorf("video content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:  ChatMessagePartTypeVideoURL,
			Video: &MessageInputVideo{MessagePartCommon: toolPart.Video.MessagePartCommon},
		}, nil
	case ToolPartTypeFile:
		if toolPart.File == nil {
			return MessageInputPart{}, fmt.Errorf("file content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type: ChatMessagePartTypeFileURL,
			File: &MessageInputFile{MessagePartCommon: toolPart.File.MessagePartCommon},
		}, nil
	default:
		return MessageInputPart{}, fmt.Errorf("unknown tool part type: %v", toolPart.Type)
	}
}

// ToMessageInputParts converts ToolOutputPart slice to MessageInputPart slice.
// This is used when passing tool results as input to the model.
//
// Parameters:
//   - None (method receiver is *ToolResult)
//
// Returns:
//   - []MessageInputPart: The converted message input parts that can be used in a Message.
//   - error: An error if conversion fails due to unknown part types or nil content fields.
//
// Example:
//
//	toolResult := &schema.ToolResult{
//	    Parts: []schema.ToolOutputPart{
//	        {Type: schema.ToolPartTypeText, Text: "Result text"},
//	        {Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{...}},
//	    },
//	}
//	inputParts, err := toolResult.ToMessageInputParts()
func (tr *ToolResult) ToMessageInputParts() ([]MessageInputPart, error) {
	if tr == nil || len(tr.Parts) == 0 {
		return nil, nil
	}
	result := make([]MessageInputPart, len(tr.Parts))
	for i, part := range tr.Parts {
		var err error
		result[i], err = convToolOutputPartToMessageInputPart(part)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Deprecated: This struct is deprecated as the MultiContent field is deprecated.
// For the image input part of the model, use MessageInputImage.
// For the image output part of the model, use MessageOutputImage.
// Choose either URL or URI.
// If your model implementation supports it, URL could embed inline image data
// as defined in RFC-2397.
type ChatMessageImageURL struct {
	// URL can either be a traditional URL or a special URL conforming to RFC-2397 (https://www.rfc-editor.org/rfc/rfc2397).
	// double check with model implementations for detailed instructions on how to use this.
	URL string `json:"url,omitempty"`

	URI string `json:"uri,omitempty"`
	// Detail is the quality of the image url.
	Detail ImageURLDetail `json:"detail,omitempty"`

	// MIMEType is the mime type of the image, eg. "image/png".
	MIMEType string `json:"mime_type,omitempty"`
	// Extra is used to store extra information for the image url.
	Extra map[string]any `json:"extra,omitempty"`
}

// ChatMessagePartType is the type of the part in a chat message.
type ChatMessagePartType string

const (
	// ChatMessagePartTypeText means the part is a text.
	ChatMessagePartTypeText ChatMessagePartType = "text"
	// ChatMessagePartTypeImageURL means the part is an image url.
	ChatMessagePartTypeImageURL ChatMessagePartType = "image_url"
	// ChatMessagePartTypeAudioURL means the part is an audio url.
	ChatMessagePartTypeAudioURL ChatMessagePartType = "audio_url"
	// ChatMessagePartTypeVideoURL means the part is a video url.
	ChatMessagePartTypeVideoURL ChatMessagePartType = "video_url"
	// ChatMessagePartTypeFileURL means the part is a file url.
	ChatMessagePartTypeFileURL ChatMessagePartType = "file_url"
)

// Deprecated: This struct is deprecated as the MultiContent field is deprecated.
// For the audio input part of the model, use MessageInputAudio.
// For the audio output part of the model, use MessageOutputAudio.
// Choose either URL or URI.
// If supported, URL may embed inline audio data per RFC-2397.
type ChatMessageAudioURL struct {
	// URL can either be a traditional URL or a special URL conforming to RFC-2397 (https://www.rfc-editor.org/rfc/rfc2397).
	// double check with model implementations for detailed instructions on how to use this.
	URL string `json:"url,omitempty"`
	URI string `json:"uri,omitempty"`

	// MIMEType is the mime type of the audio, eg. "audio/wav" or "audio/ogg".
	MIMEType string `json:"mime_type,omitempty"`
	// Extra is used to store extra information for the audio url.
	Extra map[string]any `json:"extra,omitempty"`
}

// Deprecated: This struct is deprecated as the MultiContent field is deprecated.
// For the video input part of the model, use MessageInputVideo.
// For the video output part of the model, use MessageOutputVideo.
// Choose either URL or URI.
// If supported, URL may embed inline video data per RFC-2397.
type ChatMessageVideoURL struct {
	// URL can either be a traditional URL or a special URL conforming to RFC-2397 (https://www.rfc-editor.org/rfc/rfc2397).
	// double check with model implementations for detailed instructions on how to use this.
	URL string `json:"url,omitempty"`
	URI string `json:"uri,omitempty"`

	// MIMEType is the mime type of the video, eg. "video/mp4".
	MIMEType string `json:"mime_type,omitempty"`
	// Extra is used to store extra information for the video url.
	Extra map[string]any `json:"extra,omitempty"`
}

// Deprecated: This struct is deprecated as the MultiContent field is deprecated.
// For the file input part of the model, use MessageInputFile.
// Choose either URL or URI.
type ChatMessageFileURL struct {
	URL string `json:"url,omitempty"`
	URI string `json:"uri,omitempty"`

	// MIMEType is the mime type of the file, eg. "application/pdf", "text/plain".
	MIMEType string `json:"mime_type,omitempty"`
	// Name is the name of the file.
	Name string `json:"name,omitempty"`

	// Extra is used to store extra information for the file url.
	Extra map[string]any `json:"extra,omitempty"`
}

// Deprecated: This struct is deprecated as the MultiContent field is deprecated.
// For model input, use MessageInputPart. For model output, use MessageOutputPart.
type ChatMessagePart struct {
	// Type is the type of the part, eg. "text", "image_url", "audio_url", "video_url", "file_url".
	Type ChatMessagePartType `json:"type,omitempty"`

	// Text is the text of the part, it's used when Type is "text".
	Text string `json:"text,omitempty"`

	// ImageURL is the image url of the part, it's used when Type is "image_url".
	ImageURL *ChatMessageImageURL `json:"image_url,omitempty"`
	// AudioURL is the audio url of the part, it's used when Type is "audio_url".
	AudioURL *ChatMessageAudioURL `json:"audio_url,omitempty"`
	// VideoURL is the video url of the part, it's used when Type is "video_url".
	VideoURL *ChatMessageVideoURL `json:"video_url,omitempty"`
	// FileURL is the file url of the part, it's used when Type is "file_url".
	FileURL *ChatMessageFileURL `json:"file_url,omitempty"`
}

// LogProbs is the top-level structure containing the log probability information.
type LogProbs struct {
	// Content is a list of message content tokens with log probability information.
	Content []LogProb `json:"content"`
}

// LogProb represents the probability information for a token.
type LogProb struct {
	// Token represents the text of the token, which is a contiguous sequence of characters
	// (e.g., a word, part of a word, or punctuation) as understood by the tokenization process used by the language model.
	Token string `json:"token"`
	// LogProb is the log probability of this token, if it is within the top 20 most likely tokens.
	// Otherwise, the value `-9999.0` is used to signify that the token is very unlikely.
	LogProb float64 `json:"logprob"`
	// Bytes is a list of integers representing the UTF-8 bytes representation of the token.
	// Useful in instances where characters are represented by multiple tokens and
	// their byte representations must be combined to generate the correct text
	// representation. Can be `null` if there is no bytes representation for the token.
	Bytes []int64 `json:"bytes,omitempty"` // Omitting the field if it is null
	// TopLogProbs is a list of the most likely tokens and their log probability, at this token position.
	// In rare cases, there may be fewer than the number of requested top_logprobs returned.
	TopLogProbs []TopLogProb `json:"top_logprobs"`
}

// TopLogProb describes a likely token and its log probability at a position.
type TopLogProb struct {
	// Token represents the text of the token, which is a contiguous sequence of characters
	// (e.g., a word, part of a word, or punctuation) as understood by the tokenization process used by the language model.
	Token string `json:"token"`
	// LogProb is the log probability of this token, if it is within the top 20 most likely tokens.
	// Otherwise, the value `-9999.0` is used to signify that the token is very unlikely.
	LogProb float64 `json:"logprob"`
	// Bytes is a list of integers representing the UTF-8 bytes representation of the token.
	// Useful in instances where characters are represented by multiple tokens and
	// their byte representations must be combined to generate the correct text
	// representation. Can be `null` if there is no bytes representation for the token.
	Bytes []int64 `json:"bytes,omitempty"`
}

// ResponseMeta collects meta information about a chat response.
type ResponseMeta struct {
	// FinishReason is the reason why the chat response is finished.
	// It's usually "stop", "length", "tool_calls", "content_filter", "null". This is defined by chat model implementation.
	FinishReason string `json:"finish_reason,omitempty"`
	// Usage is the token usage of the chat response, whether usage exists depends on whether the chat model implementation returns.
	Usage *TokenUsage `json:"usage,omitempty"`
	// LogProbs is Log probability information.
	LogProbs *LogProbs `json:"logprobs,omitempty"`
}

// Message denotes the data structure for model input and output, originating from either user input or model return.
// It supports both text-only and multimodal content.
//
// For text-only input from a user, use the Content field:
//
//	&schema.Message{
//		Role:    schema.User,
//		Content: "What is the capital of France?",
//	}
//
// For multimodal input from a user, use the UserInputMultiContent field.
// This allows combining text with other media like images:
//
//	&schema.Message{
//		Role: schema.User,
//		UserInputMultiContent: []schema.MessageInputPart{
//			{Type: schema.ChatMessagePartTypeText, Text: "What is in this image?"},
//			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
//				MessagePartCommon: schema.MessagePartCommon{
//					URL: toPtr("https://example.com/cat.jpg"),
//				},
//				Detail: schema.ImageURLDetailHigh,
//			}},
//		},
//	}
//
// When the model returns multimodal content, it is available in the AssistantGenMultiContent field:
//
//	&schema.Message{
//		Role: schema.Assistant,
//		AssistantGenMultiContent: []schema.MessageOutputPart{
//			{Type: schema.ChatMessagePartTypeText, Text: "Here is the generated image:"},
//			{Type: schema.ChatMessagePartTypeImage, Image: &schema.MessageOutputImage{
//				MessagePartCommon: schema.MessagePartCommon{
//					Base64Data: toPtr("base64_image_binary"),
//					MIMEType:   "image/png",
//				},
//			}},
//		},
//	}
type Message struct {
	Role RoleType `json:"role"`

	// Content is for user text input and model text output.
	Content string `json:"content"`

	// if MultiContent is not empty, use this instead of Content
	// if MultiContent is empty, use Content
	// Deprecated: Use UserInputMultiContent for user multimodal inputs and AssistantGenMultiContent for model multimodal outputs.
	MultiContent []ChatMessagePart `json:"multi_content,omitempty"`

	// UserInputMultiContent passes multimodal content provided by the user to the model.
	UserInputMultiContent []MessageInputPart `json:"user_input_multi_content,omitempty"`

	// AssistantGenMultiContent is for receiving multimodal output from the model.
	AssistantGenMultiContent []MessageOutputPart `json:"assistant_output_multi_content,omitempty"`

	Name string `json:"name,omitempty"`

	// only for AssistantMessage
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// only for ToolMessage
	ToolCallID string `json:"tool_call_id,omitempty"`
	// only for ToolMessage
	ToolName string `json:"tool_name,omitempty"`

	ResponseMeta *ResponseMeta `json:"response_meta,omitempty"`

	// ReasoningContent is the thinking process of the model, which will be included when the model returns reasoning content.
	ReasoningContent string `json:"reasoning_content,omitempty"`

	// customized information for model implementation
	Extra map[string]any `json:"extra,omitempty"`
}

// TokenUsage Represents the token usage of chat model request.
type TokenUsage struct {
	// PromptTokens is the number of prompt tokens, including all the input tokens of this request.
	PromptTokens int `json:"prompt_tokens"`
	// PromptTokenDetails is a breakdown of the prompt tokens.
	PromptTokenDetails PromptTokenDetails `json:"prompt_token_details"`
	// CompletionTokens is the number of completion tokens.
	CompletionTokens int `json:"completion_tokens"`
	// TotalTokens is the total number of tokens.
	TotalTokens int `json:"total_tokens"`
	// CompletionTokensDetails is breakdown of completion tokens.
	CompletionTokensDetails CompletionTokensDetails `json:"completion_token_details"`
}

type CompletionTokensDetails struct {
	// ReasoningTokens tokens generated by the model for reasoning.
	// This is currently supported by OpenAI, Gemini, ARK and Qwen  chat models.
	// For other models, this field will be 0.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// PromptTokenDetails provides a breakdown of prompt token usage.
type PromptTokenDetails struct {
	// Cached tokens present in the prompt.
	CachedTokens int `json:"cached_tokens"`
}

var _ MessagesTemplate = &Message{}
var _ MessagesTemplate = MessagesPlaceholder("", false)

// MessagesTemplate is the interface for messages template.
// It's used to render a template to a list of messages.
// e.g.
//
//	chatTemplate := prompt.FromMessages(
//		schema.SystemMessage("you are eino helper"),
//		schema.MessagesPlaceholder("history", false), // <= this will use the value of "history" in params
//	)
//	msgs, err := chatTemplate.Format(ctx, params)
type MessagesTemplate interface {
	Format(ctx context.Context, vs map[string]any, formatType FormatType) ([]*Message, error)
}

type messagesPlaceholder struct {
	key      string
	optional bool
}

// MessagesPlaceholder can render a placeholder to a list of messages in params.
// e.g.
//
//	placeholder := MessagesPlaceholder("history", false)
//	params := map[string]any{
//		"history": []*schema.Message{{Role: "user", Content: "what is eino?"}, {Role: "assistant", Content: "eino is a great freamwork to build llm apps"}},
//		"query": "how to use eino?",
//	}
//	chatTemplate := chatTpl := prompt.FromMessages(
//		schema.SystemMessage("you are eino helper"),
//		schema.MessagesPlaceholder("history", false), // <= this will use the value of "history" in params
//	)
//	msgs, err := chatTemplate.Format(ctx, params)
func MessagesPlaceholder(key string, optional bool) MessagesTemplate {
	return &messagesPlaceholder{
		key:      key,
		optional: optional,
	}
}

// Format just return the messages of specified key.
// because it's a placeholder.
// e.g.
//
//	placeholder := MessagesPlaceholder("history", false)
//	params := map[string]any{
//		"history": []*schema.Message{{Role: "user", Content: "what is eino?"}, {Role: "assistant", Content: "eino is a great freamwork to build llm apps"}},
//		"query": "how to use eino?",
//	}
//	msgs, err := placeholder.Format(ctx, params) // <= this will return the value of "history" in params
func (p *messagesPlaceholder) Format(_ context.Context, vs map[string]any, _ FormatType) ([]*Message, error) {
	v, ok := vs[p.key]
	if !ok {
		if p.optional {
			return []*Message{}, nil
		}

		return nil, fmt.Errorf("message placeholder format: %s not found", p.key)
	}

	msgs, ok := v.([]*Message)
	if !ok {
		return nil, fmt.Errorf("only messages can be used to format message placeholder, key: %v, actual type: %v", p.key, reflect.TypeOf(v))
	}

	return msgs, nil
}

func formatContent(content string, vs map[string]any, formatType FormatType) (string, error) {
	switch formatType {
	case FString:
		return pyfmt.Fmt(content, vs)
	case GoTemplate:
		parsedTmpl, err := template.New("template").
			Option("missingkey=error").
			Parse(content)
		if err != nil {
			return "", err
		}
		sb := new(strings.Builder)
		err = parsedTmpl.Execute(sb, vs)
		if err != nil {
			return "", err
		}
		return sb.String(), nil
	case Jinja2:
		env, err := getJinjaEnv()
		if err != nil {
			return "", err
		}
		tpl, err := env.FromString(content)
		if err != nil {
			return "", err
		}
		out, err := tpl.Execute(vs)
		if err != nil {
			return "", err
		}
		return out, nil
	default:
		return "", fmt.Errorf("unknown format type: %v", formatType)
	}
}

// Format returns the messages after rendering by the given formatType.
// e.g.
//
//	msg := schema.UserMessage("hello world, {name}")
//	msgs, err := msg.Format(ctx, map[string]any{"name": "eino"}, schema.FString) // <= this will render the content of msg by pyfmt
//	// msgs[0].Content will be "hello world, eino"
func (m *Message) Format(_ context.Context, vs map[string]any, formatType FormatType) ([]*Message, error) {
	c, err := formatContent(m.Content, vs, formatType)
	if err != nil {
		return nil, err
	}
	copied := *m
	copied.Content = c

	if len(m.MultiContent) > 0 {
		copied.MultiContent, err = formatMultiContent(m.MultiContent, vs, formatType)
		if err != nil {
			return nil, err
		}
	}

	if len(m.UserInputMultiContent) > 0 {
		copied.UserInputMultiContent, err = formatUserInputMultiContent(m.UserInputMultiContent, vs, formatType)
		if err != nil {
			return nil, err
		}
	}

	return []*Message{&copied}, nil
}

func formatMultiContent(multiContent []ChatMessagePart, vs map[string]any, formatType FormatType) ([]ChatMessagePart, error) {
	copiedMC := make([]ChatMessagePart, len(multiContent))
	copy(copiedMC, multiContent)

	for i, mc := range copiedMC {
		switch mc.Type {
		case ChatMessagePartTypeText:
			nmc, err := formatContent(mc.Text, vs, formatType)
			if err != nil {
				return nil, err
			}
			copiedMC[i].Text = nmc
		case ChatMessagePartTypeImageURL:
			if mc.ImageURL == nil {
				continue
			}
			url, err := formatContent(mc.ImageURL.URL, vs, formatType)
			if err != nil {
				return nil, err
			}
			copiedMC[i].ImageURL.URL = url
		case ChatMessagePartTypeAudioURL:
			if mc.AudioURL == nil {
				continue
			}
			url, err := formatContent(mc.AudioURL.URL, vs, formatType)
			if err != nil {
				return nil, err
			}
			copiedMC[i].AudioURL.URL = url
		case ChatMessagePartTypeVideoURL:
			if mc.VideoURL == nil {
				continue
			}
			url, err := formatContent(mc.VideoURL.URL, vs, formatType)
			if err != nil {
				return nil, err
			}
			copiedMC[i].VideoURL.URL = url
		case ChatMessagePartTypeFileURL:
			if mc.FileURL == nil {
				continue
			}
			url, err := formatContent(mc.FileURL.URL, vs, formatType)
			if err != nil {
				return nil, err
			}
			copiedMC[i].FileURL.URL = url
		}
	}

	return copiedMC, nil
}

func formatUserInputMultiContent(userInputMultiContent []MessageInputPart, vs map[string]any, formatType FormatType) ([]MessageInputPart, error) {
	copiedUIMC := make([]MessageInputPart, len(userInputMultiContent))
	copy(copiedUIMC, userInputMultiContent)

	for i, uimc := range copiedUIMC {
		switch uimc.Type {
		case ChatMessagePartTypeText:
			text, err := formatContent(uimc.Text, vs, formatType)
			if err != nil {
				return nil, err
			}
			copiedUIMC[i].Text = text
		case ChatMessagePartTypeImageURL:
			if uimc.Image == nil {
				continue
			}
			if uimc.Image.URL != nil && *uimc.Image.URL != "" {
				url, err := formatContent(*uimc.Image.URL, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].Image.URL = &url
			}
			if uimc.Image.Base64Data != nil && *uimc.Image.Base64Data != "" {
				base64data, err := formatContent(*uimc.Image.Base64Data, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].Image.Base64Data = &base64data
			}
		case ChatMessagePartTypeAudioURL:
			if uimc.Audio == nil {
				continue
			}
			if uimc.Audio.URL != nil && *uimc.Audio.URL != "" {
				url, err := formatContent(*uimc.Audio.URL, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].Audio.URL = &url
			}
			if uimc.Audio.Base64Data != nil && *uimc.Audio.Base64Data != "" {
				base64data, err := formatContent(*uimc.Audio.Base64Data, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].Audio.Base64Data = &base64data
			}
		case ChatMessagePartTypeVideoURL:
			if uimc.Video == nil {
				continue
			}
			if uimc.Video.URL != nil && *uimc.Video.URL != "" {
				url, err := formatContent(*uimc.Video.URL, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].Video.URL = &url
			}
			if uimc.Video.Base64Data != nil && *uimc.Video.Base64Data != "" {
				base64data, err := formatContent(*uimc.Video.Base64Data, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].Video.Base64Data = &base64data
			}
		case ChatMessagePartTypeFileURL:
			if uimc.File == nil {
				continue
			}
			if uimc.File.URL != nil && *uimc.File.URL != "" {
				url, err := formatContent(*uimc.File.URL, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].File.URL = &url
			}
			if uimc.File.Base64Data != nil && *uimc.File.Base64Data != "" {
				base64data, err := formatContent(*uimc.File.Base64Data, vs, formatType)
				if err != nil {
					return nil, err
				}
				copiedUIMC[i].File.Base64Data = &base64data
			}
		}
	}

	return copiedUIMC, nil
}

// String returns the string representation of the message.
// e.g.
//
//	msg := schema.UserMessage("hello world")
//	fmt.Println(msg.String()) // Output will be: `user: hello world``
//
//	msg := schema.Message{
//		Role:    schema.Tool,
//		Content: "{...}",
//		ToolCallID: "callxxxx"
//	}
//	fmt.Println(msg.String())
//	Output will be:
//		tool: {...}
//		call_id: callxxxx
func (m *Message) String() string {
	sb := &strings.Builder{}
	sb.WriteString(fmt.Sprintf("%s: %s", m.Role, m.Content))

	if len(m.UserInputMultiContent) > 0 {
		sb.WriteString("\nuser_input_multi_content:")
		for i, part := range m.UserInputMultiContent {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s", i, formatInputPart(part)))
		}
	}

	if len(m.AssistantGenMultiContent) > 0 {
		sb.WriteString("\nassistant_gen_multi_content:")
		for i, part := range m.AssistantGenMultiContent {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s", i, formatOutputPart(part)))
		}
	}

	if len(m.MultiContent) > 0 {
		sb.WriteString("\nmulti_content:")
		for i, part := range m.MultiContent {
			sb.WriteString(fmt.Sprintf("\n  [%d] %s", i, formatChatMessagePart(part)))
		}
	}

	if len(m.ReasoningContent) > 0 {
		sb.WriteString("\nreasoning content:\n")
		sb.WriteString(m.ReasoningContent)
	}
	if len(m.ToolCalls) > 0 {
		sb.WriteString("\ntool_calls:\n")
		for _, tc := range m.ToolCalls {
			if tc.Index != nil {
				sb.WriteString(fmt.Sprintf("index[%d]:", *tc.Index))
			}
			sb.WriteString(fmt.Sprintf("%+v\n", tc))
		}
	}
	if m.ToolCallID != "" {
		sb.WriteString(fmt.Sprintf("\ntool_call_id: %s", m.ToolCallID))
	}
	if m.ToolName != "" {
		sb.WriteString(fmt.Sprintf("\ntool_call_name: %s", m.ToolName))
	}
	if m.ResponseMeta != nil {
		sb.WriteString(fmt.Sprintf("\nfinish_reason: %s", m.ResponseMeta.FinishReason))
		if m.ResponseMeta.Usage != nil {
			sb.WriteString(fmt.Sprintf("\nusage: %v", m.ResponseMeta.Usage))
		}
	}

	return sb.String()
}

func formatInputPart(part MessageInputPart) string {
	switch part.Type {
	case ChatMessagePartTypeText:
		return fmt.Sprintf("text: %s", part.Text)
	case ChatMessagePartTypeImageURL:
		return fmt.Sprintf("image: %s", formatMessageInputMedia(part.Image))
	case ChatMessagePartTypeAudioURL:
		return fmt.Sprintf("audio: %s", formatMessageInputMedia(part.Audio))
	case ChatMessagePartTypeVideoURL:
		return fmt.Sprintf("video: %s", formatMessageInputMedia(part.Video))
	case ChatMessagePartTypeFileURL:
		return fmt.Sprintf("file: %s", formatMessageInputFile(part.File))
	default:
		return fmt.Sprintf("unknown type: %s", part.Type)
	}
}

func formatMessageInputMedia[T MessageInputImage | MessageInputAudio | MessageInputVideo](media *T) string {
	if media == nil {
		return "<nil>"
	}
	var parts []string
	switch v := any(media).(type) {
	case *MessageInputImage:
		if v.URL != nil {
			parts = append(parts, fmt.Sprintf("url=%s", *v.URL))
		}
		if v.Base64Data != nil {
			parts = append(parts, fmt.Sprintf("base64[%d bytes]", len(*v.Base64Data)))
		}
		if v.MIMEType != "" {
			parts = append(parts, fmt.Sprintf("mime=%s", v.MIMEType))
		}
		if v.Detail != "" {
			parts = append(parts, fmt.Sprintf("detail=%s", v.Detail))
		}
		if len(v.Extra) > 0 {
			parts = append(parts, fmt.Sprintf("extra=%v", v.Extra))
		}
	case *MessageInputAudio:
		if v.URL != nil {
			parts = append(parts, fmt.Sprintf("url=%s", *v.URL))
		}
		if v.Base64Data != nil {
			parts = append(parts, fmt.Sprintf("base64[%d bytes]", len(*v.Base64Data)))
		}
		if v.MIMEType != "" {
			parts = append(parts, fmt.Sprintf("mime=%s", v.MIMEType))
		}
		if len(v.Extra) > 0 {
			parts = append(parts, fmt.Sprintf("extra=%v", v.Extra))
		}
	case *MessageInputVideo:
		if v.URL != nil {
			parts = append(parts, fmt.Sprintf("url=%s", *v.URL))
		}
		if v.Base64Data != nil {
			parts = append(parts, fmt.Sprintf("base64[%d bytes]", len(*v.Base64Data)))
		}
		if v.MIMEType != "" {
			parts = append(parts, fmt.Sprintf("mime=%s", v.MIMEType))
		}
		if len(v.Extra) > 0 {
			parts = append(parts, fmt.Sprintf("extra=%v", v.Extra))
		}
	}
	if len(parts) == 0 {
		return "<empty>"
	}
	return strings.Join(parts, ", ")
}

func formatMessageInputFile(file *MessageInputFile) string {
	if file == nil {
		return "<nil>"
	}
	var parts []string
	if file.URL != nil {
		parts = append(parts, fmt.Sprintf("url=%s", *file.URL))
	}
	if file.Base64Data != nil {
		parts = append(parts, fmt.Sprintf("base64[%d bytes]", len(*file.Base64Data)))
	}
	if file.MIMEType != "" {
		parts = append(parts, fmt.Sprintf("mime=%s", file.MIMEType))
	}
	if file.Name != "" {
		parts = append(parts, fmt.Sprintf("name=%s", file.Name))
	}
	if len(file.Extra) > 0 {
		parts = append(parts, fmt.Sprintf("extra=%v", file.Extra))
	}
	if len(parts) == 0 {
		return "<empty>"
	}
	return strings.Join(parts, ", ")
}

func formatOutputPart(part MessageOutputPart) string {
	switch part.Type {
	case ChatMessagePartTypeText:
		return fmt.Sprintf("text: %s", part.Text)
	case ChatMessagePartTypeImageURL:
		return fmt.Sprintf("image: %s", formatMessageOutputMedia(part.Image))
	case ChatMessagePartTypeAudioURL:
		return fmt.Sprintf("audio: %s", formatMessageOutputMedia(part.Audio))
	case ChatMessagePartTypeVideoURL:
		return fmt.Sprintf("video: %s", formatMessageOutputMedia(part.Video))
	default:
		return fmt.Sprintf("unknown type: %s", part.Type)
	}
}

func formatMessageOutputMedia[T MessageOutputImage | MessageOutputAudio | MessageOutputVideo](media *T) string {
	if media == nil {
		return "<nil>"
	}
	var parts []string
	switch v := any(media).(type) {
	case *MessageOutputImage:
		if v.URL != nil {
			parts = append(parts, fmt.Sprintf("url=%s", *v.URL))
		}
		if v.Base64Data != nil {
			parts = append(parts, fmt.Sprintf("base64[%d bytes]", len(*v.Base64Data)))
		}
		if v.MIMEType != "" {
			parts = append(parts, fmt.Sprintf("mime=%s", v.MIMEType))
		}
		if len(v.Extra) > 0 {
			parts = append(parts, fmt.Sprintf("extra=%v", v.Extra))
		}
	case *MessageOutputAudio:
		if v.URL != nil {
			parts = append(parts, fmt.Sprintf("url=%s", *v.URL))
		}
		if v.Base64Data != nil {
			parts = append(parts, fmt.Sprintf("base64[%d bytes]", len(*v.Base64Data)))
		}
		if v.MIMEType != "" {
			parts = append(parts, fmt.Sprintf("mime=%s", v.MIMEType))
		}
		if len(v.Extra) > 0 {
			parts = append(parts, fmt.Sprintf("extra=%v", v.Extra))
		}
	case *MessageOutputVideo:
		if v.URL != nil {
			parts = append(parts, fmt.Sprintf("url=%s", *v.URL))
		}
		if v.Base64Data != nil {
			parts = append(parts, fmt.Sprintf("base64[%d bytes]", len(*v.Base64Data)))
		}
		if v.MIMEType != "" {
			parts = append(parts, fmt.Sprintf("mime=%s", v.MIMEType))
		}
		if len(v.Extra) > 0 {
			parts = append(parts, fmt.Sprintf("extra=%v", v.Extra))
		}
	}
	if len(parts) == 0 {
		return "<empty>"
	}
	return strings.Join(parts, ", ")
}

func formatChatMessagePart(part ChatMessagePart) string {
	switch part.Type {
	case ChatMessagePartTypeText:
		return fmt.Sprintf("text: %s", part.Text)
	case ChatMessagePartTypeImageURL:
		if part.ImageURL != nil {
			return fmt.Sprintf("image_url: %s", part.ImageURL.URL)
		}
		return "image_url: <nil>"
	case ChatMessagePartTypeAudioURL:
		if part.AudioURL != nil {
			return fmt.Sprintf("audio_url: %s", part.AudioURL.URL)
		}
		return "audio_url: <nil>"
	case ChatMessagePartTypeVideoURL:
		if part.VideoURL != nil {
			return fmt.Sprintf("video_url: %s", part.VideoURL.URL)
		}
		return "video_url: <nil>"
	case ChatMessagePartTypeFileURL:
		if part.FileURL != nil {
			return fmt.Sprintf("file_url: %s", part.FileURL.URL)
		}
		return "file_url: <nil>"
	default:
		return fmt.Sprintf("unknown type: %s", part.Type)
	}
}

// SystemMessage represents a message with Role "system".
func SystemMessage(content string) *Message {
	return &Message{
		Role:    System,
		Content: content,
	}
}

// AssistantMessage represents a message with Role "assistant".
func AssistantMessage(content string, toolCalls []ToolCall) *Message {
	return &Message{
		Role:      Assistant,
		Content:   content,
		ToolCalls: toolCalls,
	}
}

// UserMessage represents a message with Role "user".
func UserMessage(content string) *Message {
	return &Message{
		Role:    User,
		Content: content,
	}

}

type toolMessageOptions struct {
	toolName string
}

// ToolMessageOption defines a option for ToolMessage
type ToolMessageOption func(*toolMessageOptions)

// WithToolName returns a ToolMessageOption that sets the tool call name.
func WithToolName(name string) ToolMessageOption {
	return func(o *toolMessageOptions) {
		o.toolName = name
	}
}

// ToolMessage represents a message with Role "tool".
func ToolMessage(content string, toolCallID string, opts ...ToolMessageOption) *Message {
	o := &toolMessageOptions{}
	for _, opt := range opts {
		opt(o)
	}
	return &Message{
		Role:       Tool,
		Content:    content,
		ToolCallID: toolCallID,
		ToolName:   o.toolName,
	}
}

// ConcatToolResults merges multiple ToolResult chunks into a single ToolResult.
// It collects all ToolOutputParts from the input chunks and merges contiguous text parts within each chunk.
//
// Merge rules:
//   - Text parts: Contiguous text parts within each chunk are concatenated into a single text part.
//   - Non-text parts (image, audio, video, file): These parts are kept as-is without merging.
//     Each non-text part type can only appear in one chunk; if the same non-text type appears
//     in multiple chunks, an error is returned.
//
// This function is primarily used in streaming scenarios where tool output is delivered
// in multiple chunks that need to be merged into a complete result.
//
// Parameters:
//   - chunks: A slice of ToolResult pointers representing sequential chunks from a stream.
//     Nil chunks and chunks with empty Parts are safely ignored.
//
// Returns:
//   - *ToolResult: The merged ToolResult containing all content from the chunks.
//     Returns an empty ToolResult if chunks is empty or all chunks are nil/empty.
//   - error: An error if the same non-text part type appears in multiple chunks.
func ConcatToolResults(chunks []*ToolResult) (*ToolResult, error) {
	if len(chunks) == 0 {
		return &ToolResult{}, nil
	}

	nonTextPartTypes := make(map[ToolPartType]int)

	var allParts []ToolOutputPart
	for chunkIdx, chunk := range chunks {
		if chunk == nil || len(chunk.Parts) == 0 {
			continue
		}

		for _, part := range chunk.Parts {
			if part.Type != ToolPartTypeText {
				if prevChunkIdx, exists := nonTextPartTypes[part.Type]; exists {
					return nil, fmt.Errorf("conflicting %s parts found in chunk %d and chunk %d: "+
						"non-text modality parts cannot appear in multiple chunks", part.Type, prevChunkIdx, chunkIdx)
				}
				nonTextPartTypes[part.Type] = chunkIdx
			}
		}

		mergedChunkParts := mergeTextPartsInChunk(chunk.Parts)
		allParts = append(allParts, mergedChunkParts...)
	}

	if len(allParts) == 0 {
		return &ToolResult{}, nil
	}

	return &ToolResult{Parts: allParts}, nil
}

func mergeTextPartsInChunk(parts []ToolOutputPart) []ToolOutputPart {
	if len(parts) == 0 {
		return nil
	}

	merged := make([]ToolOutputPart, 0, len(parts))
	i := 0

	for i < len(parts) {
		currentPart := parts[i]

		if currentPart.Type == ToolPartTypeText {
			end := i + 1
			for end < len(parts) && parts[end].Type == ToolPartTypeText {
				end++
			}

			if end == i+1 {
				merged = append(merged, currentPart)
			} else {
				var sb strings.Builder
				for k := i; k < end; k++ {
					sb.WriteString(parts[k].Text)
				}
				mergedPart := ToolOutputPart{
					Type: ToolPartTypeText,
					Text: sb.String(),
				}
				merged = append(merged, mergedPart)
			}
			i = end
		} else {
			merged = append(merged, currentPart)
			i++
		}
	}

	return merged
}

func concatToolCalls(chunks []ToolCall) ([]ToolCall, error) {
	var merged []ToolCall
	m := make(map[int][]int)
	for i := range chunks {
		index := chunks[i].Index
		if index == nil {
			merged = append(merged, chunks[i])
		} else {
			m[*index] = append(m[*index], i)
		}
	}

	var args strings.Builder
	for k, v := range m {
		index := k
		toolCall := ToolCall{Index: &index}
		if len(v) > 0 {
			toolCall = chunks[v[0]]
		}

		args.Reset()
		toolID, toolType, toolName := "", "", "" // these field will output atomically in any chunk

		for _, n := range v {
			chunk := chunks[n]
			if chunk.ID != "" {
				if toolID == "" {
					toolID = chunk.ID
				} else if toolID != chunk.ID {
					return nil, fmt.Errorf("cannot concat ToolCalls with different tool id: '%s' '%s'", toolID, chunk.ID)
				}

			}

			if chunk.Type != "" {
				if toolType == "" {
					toolType = chunk.Type
				} else if toolType != chunk.Type {
					return nil, fmt.Errorf("cannot concat ToolCalls with different tool type: '%s' '%s'", toolType, chunk.Type)
				}
			}

			if chunk.Function.Name != "" {
				if toolName == "" {
					toolName = chunk.Function.Name
				} else if toolName != chunk.Function.Name {
					return nil, fmt.Errorf("cannot concat ToolCalls with different tool name: '%s' '%s'", toolName, chunk.Function.Name)
				}
			}

			if chunk.Function.Arguments != "" {
				_, err := args.WriteString(chunk.Function.Arguments)
				if err != nil {
					return nil, err
				}
			}
		}

		toolCall.ID = toolID
		toolCall.Type = toolType
		toolCall.Function.Name = toolName
		toolCall.Function.Arguments = args.String()

		merged = append(merged, toolCall)
	}

	if len(merged) > 1 {
		sort.SliceStable(merged, func(i, j int) bool {
			iVal, jVal := merged[i].Index, merged[j].Index
			if iVal == nil && jVal == nil {
				return false
			} else if iVal == nil && jVal != nil {
				return true
			} else if iVal != nil && jVal == nil {
				return false
			}

			return *iVal < *jVal
		})
	}

	return merged, nil
}

func concatAssistantMultiContent(parts []MessageOutputPart) ([]MessageOutputPart, error) {
	if len(parts) == 0 {
		return parts, nil
	}

	merged := make([]MessageOutputPart, 0, len(parts))
	i := 0
	for i < len(parts) {
		currentPart := parts[i]
		start := i

		if currentPart.Type == ChatMessagePartTypeText {
			// --- Text Merging ---
			// Find end of contiguous text block
			end := start + 1
			for end < len(parts) && parts[end].Type == ChatMessagePartTypeText {
				end++
			}

			// If only one part, just append it
			if end == start+1 {
				merged = append(merged, currentPart)
			} else {
				// Multiple parts to merge
				var sb strings.Builder
				for k := start; k < end; k++ {
					sb.WriteString(parts[k].Text)
				}
				mergedPart := MessageOutputPart{
					Type: ChatMessagePartTypeText,
					Text: sb.String(),
				}
				merged = append(merged, mergedPart)
			}
			i = end
		} else if isBase64MessageOutputAudioPart(currentPart) {
			// --- Audio Merging ---
			// Find end of contiguous audio block
			end := start + 1
			for end < len(parts) && isBase64MessageOutputAudioPart(parts[end]) {
				end++
			}

			// If only one part, just append it
			if end == start+1 {
				merged = append(merged, currentPart)
			} else {
				// Multiple parts to merge
				var b64Builder strings.Builder
				var mimeType string
				extraList := make([]map[string]any, 0, end-start)

				for k := start; k < end; k++ {
					audioPart := parts[k].Audio
					if audioPart.Base64Data != nil {
						b64Builder.WriteString(*audioPart.Base64Data)
					}
					if mimeType == "" {
						mimeType = audioPart.MIMEType
					}
					if len(audioPart.Extra) > 0 {
						extraList = append(extraList, audioPart.Extra)
					}
				}

				var mergedExtra map[string]any
				var err error
				if len(extraList) > 0 {
					mergedExtra, err = concatExtra(extraList)
					if err != nil {
						return nil, fmt.Errorf("failed to concat audio extra: %w", err)
					}
				}

				mergedB64 := b64Builder.String()
				mergedPart := MessageOutputPart{
					Type: ChatMessagePartTypeAudioURL,
					Audio: &MessageOutputAudio{
						MessagePartCommon: MessagePartCommon{
							Base64Data: &mergedB64,
							MIMEType:   mimeType,
							Extra:      mergedExtra,
						},
					},
				}
				merged = append(merged, mergedPart)
			}
			i = end
		} else {
			// --- Non-mergeable part ---
			merged = append(merged, currentPart)
			i++
		}
	}

	return merged, nil
}
func isBase64MessageOutputAudioPart(part MessageOutputPart) bool {
	return part.Type == ChatMessagePartTypeAudioURL &&
		part.Audio != nil &&
		part.Audio.Base64Data != nil &&
		part.Audio.URL == nil
}

func concatUserMultiContent(parts []MessageInputPart) ([]MessageInputPart, error) {
	if len(parts) == 0 {
		return parts, nil
	}

	merged := make([]MessageInputPart, 0, len(parts))
	i := 0
	for i < len(parts) {
		currentPart := parts[i]

		if currentPart.Type == ChatMessagePartTypeText {
			end := i + 1
			for end < len(parts) && parts[end].Type == ChatMessagePartTypeText {
				end++
			}

			if end == i+1 {
				merged = append(merged, currentPart)
			} else {
				var sb strings.Builder
				for k := i; k < end; k++ {
					sb.WriteString(parts[k].Text)
				}
				mergedPart := MessageInputPart{
					Type: ChatMessagePartTypeText,
					Text: sb.String(),
				}
				merged = append(merged, mergedPart)
			}
			i = end
		} else {

			merged = append(merged, currentPart)
			i++
		}
	}

	return merged, nil
}

func concatExtra(extraList []map[string]any) (map[string]any, error) {
	if len(extraList) == 1 {
		return generic.CopyMap(extraList[0]), nil
	}

	return internal.ConcatItems(extraList)
}

// ConcatMessages concat messages with the same role and name.
// It will concat tool calls with the same index.
// It will return an error if the messages have different roles or names.
// It's useful for concatenating messages from a stream.
// e.g.
//
//	msgs := []*Message{}
//	for {
//		msg, err := stream.Recv()
//		if errors.Is(err, io.EOF) {
//			break
//		}
//		if err != nil {...}
//		msgs = append(msgs, msg)
//	}
//
// concatedMsg, err := ConcatMessages(msgs) // concatedMsg.Content will be full content of all messages
func ConcatMessages(msgs []*Message) (*Message, error) {
	var (
		contents                      []string
		contentLen                    int
		reasoningContents             []string
		reasoningContentLen           int
		toolCalls                     []ToolCall
		multiContentParts             []ChatMessagePart
		assistantGenMultiContentParts []MessageOutputPart
		userInputMultiContentParts    []MessageInputPart
		ret                           = Message{}
		extraList                     = make([]map[string]any, 0, len(msgs))
	)

	for idx, msg := range msgs {
		if msg == nil {
			return nil, fmt.Errorf("unexpected nil chunk in message stream, index: %d", idx)
		}

		if msg.Role != "" {
			if ret.Role == "" {
				ret.Role = msg.Role
			} else if ret.Role != msg.Role {
				return nil, fmt.Errorf("cannot concat messages with "+
					"different roles: '%s' '%s'", ret.Role, msg.Role)
			}
		}

		if msg.Name != "" {
			if ret.Name == "" {
				ret.Name = msg.Name
			} else if ret.Name != msg.Name {
				return nil, fmt.Errorf("cannot concat messages with"+
					" different names: '%s' '%s'", ret.Name, msg.Name)
			}
		}

		if msg.ToolCallID != "" {
			if ret.ToolCallID == "" {
				ret.ToolCallID = msg.ToolCallID
			} else if ret.ToolCallID != msg.ToolCallID {
				return nil, fmt.Errorf("cannot concat messages with"+
					" different toolCallIDs: '%s' '%s'", ret.ToolCallID, msg.ToolCallID)
			}
		}
		if msg.ToolName != "" {
			if ret.ToolName == "" {
				ret.ToolName = msg.ToolName
			} else if ret.ToolName != msg.ToolName {
				return nil, fmt.Errorf("cannot concat messages with"+
					" different toolNames: '%s' '%s'", ret.ToolCallID, msg.ToolCallID)
			}
		}

		if msg.Content != "" {
			contents = append(contents, msg.Content)
			contentLen += len(msg.Content)
		}
		if msg.ReasoningContent != "" {
			reasoningContents = append(reasoningContents, msg.ReasoningContent)
			reasoningContentLen += len(msg.ReasoningContent)
		}

		if len(msg.ToolCalls) > 0 {
			toolCalls = append(toolCalls, msg.ToolCalls...)
		}

		if len(msg.Extra) > 0 {
			extraList = append(extraList, msg.Extra)
		}

		// The 'MultiContent' field is deprecated but is kept for backward compatibility.
		if len(msg.MultiContent) > 0 {
			multiContentParts = append(multiContentParts, msg.MultiContent...)
		}

		if len(msg.AssistantGenMultiContent) > 0 {
			assistantGenMultiContentParts = append(assistantGenMultiContentParts, msg.AssistantGenMultiContent...)
		}
		if len(msg.UserInputMultiContent) > 0 {
			userInputMultiContentParts = append(userInputMultiContentParts, msg.UserInputMultiContent...)
		}
		if msg.ResponseMeta != nil && ret.ResponseMeta == nil {
			ret.ResponseMeta = &ResponseMeta{}
		}

		if msg.ResponseMeta != nil && ret.ResponseMeta != nil {
			// keep the last FinishReason with a valid value.
			if msg.ResponseMeta.FinishReason != "" {
				ret.ResponseMeta.FinishReason = msg.ResponseMeta.FinishReason
			}

			if msg.ResponseMeta.Usage != nil {
				if ret.ResponseMeta.Usage == nil {
					ret.ResponseMeta.Usage = &TokenUsage{}
				}

				if msg.ResponseMeta.Usage.PromptTokens > ret.ResponseMeta.Usage.PromptTokens {
					ret.ResponseMeta.Usage.PromptTokens = msg.ResponseMeta.Usage.PromptTokens
				}
				if msg.ResponseMeta.Usage.CompletionTokens > ret.ResponseMeta.Usage.CompletionTokens {
					ret.ResponseMeta.Usage.CompletionTokens = msg.ResponseMeta.Usage.CompletionTokens
				}

				if msg.ResponseMeta.Usage.TotalTokens > ret.ResponseMeta.Usage.TotalTokens {
					ret.ResponseMeta.Usage.TotalTokens = msg.ResponseMeta.Usage.TotalTokens
				}

				if msg.ResponseMeta.Usage.PromptTokenDetails.CachedTokens > ret.ResponseMeta.Usage.PromptTokenDetails.CachedTokens {
					ret.ResponseMeta.Usage.PromptTokenDetails.CachedTokens = msg.ResponseMeta.Usage.PromptTokenDetails.CachedTokens
				}
			}

			if msg.ResponseMeta.LogProbs != nil {
				if ret.ResponseMeta.LogProbs == nil {
					ret.ResponseMeta.LogProbs = &LogProbs{}
				}

				ret.ResponseMeta.LogProbs.Content = append(ret.ResponseMeta.LogProbs.Content, msg.ResponseMeta.LogProbs.Content...)
			}

		}
	}

	if len(contents) > 0 {
		var sb strings.Builder
		sb.Grow(contentLen)
		for _, content := range contents {
			_, err := sb.WriteString(content)
			if err != nil {
				return nil, err
			}
		}

		ret.Content = sb.String()
	}
	if len(reasoningContents) > 0 {
		var sb strings.Builder
		sb.Grow(reasoningContentLen)
		for _, rc := range reasoningContents {
			_, err := sb.WriteString(rc)
			if err != nil {
				return nil, err
			}
		}

		ret.ReasoningContent = sb.String()
	}

	if len(toolCalls) > 0 {
		merged, err := concatToolCalls(toolCalls)
		if err != nil {
			return nil, err
		}

		ret.ToolCalls = merged
	}

	if len(extraList) > 0 {
		extra, err := concatExtra(extraList)
		if err != nil {
			return nil, fmt.Errorf("failed to concat message's extra: %w", err)
		}

		if len(extra) > 0 {
			ret.Extra = extra
		}
	}

	if len(multiContentParts) > 0 {
		ret.MultiContent = multiContentParts
	}

	if len(assistantGenMultiContentParts) > 0 {
		merged, err := concatAssistantMultiContent(assistantGenMultiContentParts)
		if err != nil {
			return nil, fmt.Errorf("failed to concat message's assistant multicontent: %w", err)
		}
		ret.AssistantGenMultiContent = merged
	}

	if len(userInputMultiContentParts) > 0 {
		merged, err := concatUserMultiContent(userInputMultiContentParts)
		if err != nil {
			return nil, fmt.Errorf("failed to concat message's user multicontent: %w", err)
		}
		ret.UserInputMultiContent = merged
	}

	return &ret, nil
}

// ConcatMessageStream drains a stream of messages and returns a single
// concatenated message representing the merged content.
func ConcatMessageStream(s *StreamReader[*Message]) (*Message, error) {
	defer s.Close()

	var msgs []*Message
	for {
		msg, err := s.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}

			return nil, err
		}

		msgs = append(msgs, msg)
	}

	return ConcatMessages(msgs)
}

// custom jinja env
var jinjaEnvOnce sync.Once
var jinjaEnv *gonja.Environment
var envInitErr error

const (
	jinjaInclude = "include"
	jinjaExtends = "extends"
	jinjaImport  = "import"
	jinjaFrom    = "from"
)

func getJinjaEnv() (*gonja.Environment, error) {
	jinjaEnvOnce.Do(func() {
		jinjaEnv = gonja.NewEnvironment(config.DefaultConfig, gonja.DefaultLoader)
		formatInitError := "init jinja env fail: %w"
		var err error
		if jinjaEnv.Statements.Exists(jinjaInclude) {
			err = jinjaEnv.Statements.Replace(jinjaInclude, func(parser *parser.Parser, args *parser.Parser) (nodes.Statement, error) {
				return nil, fmt.Errorf("keyword[include] has been disabled")
			})
			if err != nil {
				envInitErr = fmt.Errorf(formatInitError, err)
				return
			}
		}
		if jinjaEnv.Statements.Exists(jinjaExtends) {
			err = jinjaEnv.Statements.Replace(jinjaExtends, func(parser *parser.Parser, args *parser.Parser) (nodes.Statement, error) {
				return nil, fmt.Errorf("keyword[extends] has been disabled")
			})
			if err != nil {
				envInitErr = fmt.Errorf(formatInitError, err)
				return
			}
		}
		if jinjaEnv.Statements.Exists(jinjaFrom) {
			err = jinjaEnv.Statements.Replace(jinjaFrom, func(parser *parser.Parser, args *parser.Parser) (nodes.Statement, error) {
				return nil, fmt.Errorf("keyword[from] has been disabled")
			})
			if err != nil {
				envInitErr = fmt.Errorf(formatInitError, err)
				return
			}
		}
		if jinjaEnv.Statements.Exists(jinjaImport) {
			err = jinjaEnv.Statements.Replace(jinjaImport, func(parser *parser.Parser, args *parser.Parser) (nodes.Statement, error) {
				return nil, fmt.Errorf("keyword[import] has been disabled")
			})
			if err != nil {
				envInitErr = fmt.Errorf(formatInitError, err)
				return
			}
		}
	})
	return jinjaEnv, envInitErr
}
