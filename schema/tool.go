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
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/eino-contrib/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// DataType is the type of the parameter.
// It must be one of the following values: "object", "number", "integer", "string", "array", "null", "boolean", which is the same as the type of the parameter in JSONSchema.
type DataType string

// Supported JSONSchema data types for tool parameters.
const (
	Object  DataType = "object"
	Number  DataType = "number"
	Integer DataType = "integer"
	String  DataType = "string"
	Array   DataType = "array"
	Null    DataType = "null"
	Boolean DataType = "boolean"
)

// ToolChoice controls how the model uses the tools provided to it.
// Pass as part of the model option via [model.WithToolChoice].
type ToolChoice string

const (
	// ToolChoiceForbidden instructs the model not to call any tools, even if
	// tools are bound. The model responds with a plain text message instead.
	// Corresponds to "none" in OpenAI Chat Completion.
	ToolChoiceForbidden ToolChoice = "forbidden"

	// ToolChoiceAllowed lets the model decide: it may generate a plain message
	// or call one or more tools. This is the default when tools are provided.
	// Corresponds to "auto" in OpenAI Chat Completion.
	ToolChoiceAllowed ToolChoice = "allowed"

	// ToolChoiceForced requires the model to call at least one tool. Use this
	// when you want to guarantee structured output via tool calling.
	// Corresponds to "required" in OpenAI Chat Completion.
	ToolChoiceForced ToolChoice = "forced"
)

type AgenticToolChoice struct {
	// Type is the tool choice mode.
	Type ToolChoice

	// Allowed optionally specifies the list of tools that the model is permitted to call.
	// Optional.
	Allowed *AgenticAllowedToolChoice

	// Forced optionally specifies the list of tools that the model is required to call.
	// Optional.
	Forced *AgenticForcedToolChoice
}

// AgenticAllowedToolChoice specifies a list of allowed tools for the model.
type AgenticAllowedToolChoice struct {
	// Tools is the list of allowed tools for the model to call.
	// Optional.
	Tools []*AllowedTool
}

// AgenticForcedToolChoice specifies a list of tools that the model must call.
type AgenticForcedToolChoice struct {
	// Tools is the list of tools that the model must call.
	// Optional.
	Tools []*AllowedTool
}

// AllowedTool represents a tool that the model is allowed or forced to call.
// Exactly one of FunctionName, MCPTool, or ServerTool must be specified.
type AllowedTool struct {
	// FunctionName specifies a function tool by name.
	FunctionName string

	// MCPTool specifies an MCP tool.
	MCPTool *AllowedMCPTool

	// ServerTool specifies a server tool.
	ServerTool *AllowedServerTool
}

// AllowedMCPTool contains the information for identifying an MCP tool.
type AllowedMCPTool struct {
	// ServerLabel is the label of the MCP server.
	ServerLabel string
	// Name is the name of the MCP tool.
	Name string
}

// AllowedServerTool contains the information for identifying a server tool.
type AllowedServerTool struct {
	// Name is the name of the server tool.
	Name string
}

// ToolInfo is the information of a tool.
// ToolInfo describes a tool that can be passed to a ChatModel via
// [ToolCallingChatModel.WithTools] or [ChatModel.BindTools].
//
// Name should be concise and unique within the tool set. Desc should explain
// when and why to use the tool; few-shot examples in Desc significantly improve
// model accuracy. ParamsOneOf may be nil for tools that take no arguments.
type ToolInfo struct {
	// The unique name of the tool that clearly communicates its purpose.
	Name string
	// Used to tell the model how/when/why to use the tool.
	// You can provide few-shot examples as a part of the description.
	Desc string
	// Extra is the extra information for the tool.
	Extra map[string]any

	// The parameters the functions accepts (different models may require different parameter types).
	// can be described in two ways:
	//  - use params: schema.NewParamsOneOfByParams(params)
	//  - use jsonschema: schema.NewParamsOneOfByJSONSchema(jsonschema)
	// If is nil, signals that the tool does not need any input parameter
	*ParamsOneOf
}

type toolInfoForJSON struct {
	Name           string                    `json:"name,omitempty"`
	Desc           string                    `json:"desc,omitempty"`
	Extra          map[string]any            `json:"extra,omitempty"`
	HasParamsOneOf bool                      `json:"has_params_one_of,omitempty"`
	Params         map[string]*ParameterInfo `json:"params,omitempty"`
	JSONSchema     *jsonschema.Schema        `json:"json_schema,omitempty"`
}

type toolInfoForGob struct {
	Name           string
	Desc           string
	Extra          map[string]any
	HasParamsOneOf bool
	Params         map[string]*ParameterInfo
	JSONSchema     *string
}

func (t *ToolInfo) MarshalJSON() ([]byte, error) {
	tmp := &toolInfoForJSON{
		Name:  t.Name,
		Desc:  t.Desc,
		Extra: t.Extra,
	}
	if t.ParamsOneOf != nil {
		tmp.HasParamsOneOf = true
		tmp.Params = t.ParamsOneOf.params
		tmp.JSONSchema = t.ParamsOneOf.jsonschema
	}
	return json.Marshal(tmp)
}

func (t *ToolInfo) UnmarshalJSON(data []byte) error {
	tmp := &toolInfoForJSON{}
	if err := json.Unmarshal(data, tmp); err != nil {
		return err
	}
	t.Name = tmp.Name
	t.Desc = tmp.Desc
	t.Extra = tmp.Extra
	if tmp.HasParamsOneOf {
		t.ParamsOneOf = &ParamsOneOf{
			params:     tmp.Params,
			jsonschema: tmp.JSONSchema,
		}
	}
	return nil
}

func (t *ToolInfo) GobEncode() ([]byte, error) {
	tmp := &toolInfoForGob{
		Name:  t.Name,
		Desc:  t.Desc,
		Extra: t.Extra,
	}
	if t.ParamsOneOf != nil {
		tmp.HasParamsOneOf = true
		tmp.Params = t.ParamsOneOf.params
		if t.ParamsOneOf.jsonschema != nil {
			b, err := json.Marshal(t.ParamsOneOf.jsonschema)
			if err != nil {
				return nil, err
			}
			str := string(b)
			tmp.JSONSchema = &str
		}
	}
	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(tmp); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (t *ToolInfo) GobDecode(b []byte) error {
	tmp := &toolInfoForGob{}
	if err := gob.NewDecoder(bytes.NewBuffer(b)).Decode(tmp); err != nil {
		return err
	}
	t.Name = tmp.Name
	t.Desc = tmp.Desc
	t.Extra = tmp.Extra
	if !tmp.HasParamsOneOf {
		return nil
	}
	t.ParamsOneOf = &ParamsOneOf{
		params: tmp.Params,
	}
	if tmp.JSONSchema != nil {
		s := &jsonschema.Schema{}
		if err := json.Unmarshal([]byte(*tmp.JSONSchema), s); err != nil {
			return err
		}
		t.ParamsOneOf.jsonschema = s
	}
	return nil
}

// ParameterInfo is the information of a parameter.
// It is used to describe the parameters of a tool.
type ParameterInfo struct {
	// The type of the parameter.
	Type DataType
	// The element type of the parameter, only for array.
	ElemInfo *ParameterInfo
	// The sub parameters of the parameter, only for object.
	SubParams map[string]*ParameterInfo
	// The description of the parameter.
	Desc string
	// The enum values of the parameter, only for string.
	Enum []string
	// Whether the parameter is required.
	Required bool
}

// ParamsOneOf holds a tool's parameter schema using exactly one of two
// representations. Choose the one that best fits your needs:
//
//  1. [NewParamsOneOfByParams] — lightweight: describe parameters as a
//     map[string]*[ParameterInfo]. Covers the most common cases (scalars,
//     arrays, nested objects, enums, required flags).
//
//  2. [NewParamsOneOfByJSONSchema] — powerful: supply a full
//     *jsonschema.Schema (JSON Schema 2020-12). Required when you need
//     features not expressible via ParameterInfo, such as anyOf, oneOf, or
//     $defs references. [utils.InferTool] generates this form automatically
//     from Go struct tags.
//
// You must use exactly one constructor — setting both fields is invalid.
// If ParamsOneOf is nil, the tool takes no input parameters.
type ParamsOneOf struct {
	// use NewParamsOneOfByParams to set this field
	params map[string]*ParameterInfo

	jsonschema *jsonschema.Schema
}

// NewParamsOneOfByParams creates a ParamsOneOf with map[string]*ParameterInfo.
func NewParamsOneOfByParams(params map[string]*ParameterInfo) *ParamsOneOf {
	return &ParamsOneOf{
		params: params,
	}
}

// NewParamsOneOfByJSONSchema creates a ParamsOneOf with *jsonschema.Schema.
func NewParamsOneOfByJSONSchema(s *jsonschema.Schema) *ParamsOneOf {
	return &ParamsOneOf{
		jsonschema: s,
	}
}

// ToJSONSchema parses ParamsOneOf, converts the parameter description that user actually provides, into the format ready to be passed to Model.
func (p *ParamsOneOf) ToJSONSchema() (*jsonschema.Schema, error) {
	if p == nil {
		return nil, nil
	}

	if p.params != nil {
		sc := &jsonschema.Schema{
			Properties: orderedmap.New[string, *jsonschema.Schema](),
			Type:       string(Object),
			Required:   make([]string, 0, len(p.params)),
		}

		keys := make([]string, 0, len(p.params))
		for k := range p.params {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := p.params[k]
			sc.Properties.Set(k, paramInfoToJSONSchema(v))
			if v.Required {
				sc.Required = append(sc.Required, k)
			}
		}

		return sc, nil
	}

	return p.jsonschema, nil
}

func paramInfoToJSONSchema(paramInfo *ParameterInfo) *jsonschema.Schema {
	js := &jsonschema.Schema{
		Type:        string(paramInfo.Type),
		Description: paramInfo.Desc,
	}

	if len(paramInfo.Enum) > 0 {
		js.Enum = make([]any, len(paramInfo.Enum))
		for i, enum := range paramInfo.Enum {
			js.Enum[i] = enum
		}
	}

	if paramInfo.ElemInfo != nil {
		js.Items = paramInfoToJSONSchema(paramInfo.ElemInfo)
	}

	if len(paramInfo.SubParams) > 0 {
		required := make([]string, 0, len(paramInfo.SubParams))
		js.Properties = orderedmap.New[string, *jsonschema.Schema]()
		keys := make([]string, 0, len(paramInfo.SubParams))
		for k := range paramInfo.SubParams {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := paramInfo.SubParams[k]
			item := paramInfoToJSONSchema(v)
			js.Properties.Set(k, item)
			if v.Required {
				required = append(required, k)
			}
		}

		js.Required = required
	}

	return js
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

	// ToolPartTypeToolSearchResult means the part contains tool search results.
	ToolPartTypeToolSearchResult ToolPartType = "tool_search_result"
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

// ToolSearchResult represents the result of a tool search operation.
// When a model issues a tool search call, the framework searches for matching tools
// and returns the results via this struct.
type ToolSearchResult struct {
	// Tools contains the full definitions of matched tools that were not previously
	// registered. Their complete definitions are required so that the model can
	// understand their parameters and usage.
	Tools []*ToolInfo
}

func (t *ToolSearchResult) String() string {
	sb := new(strings.Builder)
	sb.WriteString("ToolSearchResult[")
	for _, tool := range t.Tools {
		sb.WriteString(tool.Name)
		sb.WriteString(",")
	}
	sb.WriteString("]")
	return sb.String()
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

	// ToolSearchResult holds the tool search results, used when Type is ToolPartTypeToolSearchResult.
	ToolSearchResult *ToolSearchResult `json:"tool_search_result,omitempty"`

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
			Type:  ChatMessagePartTypeText,
			Text:  toolPart.Text,
			Extra: toolPart.Extra,
		}, nil
	case ToolPartTypeImage:
		if toolPart.Image == nil {
			return MessageInputPart{}, fmt.Errorf("image content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:  ChatMessagePartTypeImageURL,
			Image: &MessageInputImage{MessagePartCommon: toolPart.Image.MessagePartCommon},
			Extra: toolPart.Extra,
		}, nil
	case ToolPartTypeAudio:
		if toolPart.Audio == nil {
			return MessageInputPart{}, fmt.Errorf("audio content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:  ChatMessagePartTypeAudioURL,
			Audio: &MessageInputAudio{MessagePartCommon: toolPart.Audio.MessagePartCommon},
			Extra: toolPart.Extra,
		}, nil
	case ToolPartTypeVideo:
		if toolPart.Video == nil {
			return MessageInputPart{}, fmt.Errorf("video content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:  ChatMessagePartTypeVideoURL,
			Video: &MessageInputVideo{MessagePartCommon: toolPart.Video.MessagePartCommon},
			Extra: toolPart.Extra,
		}, nil
	case ToolPartTypeFile:
		if toolPart.File == nil {
			return MessageInputPart{}, fmt.Errorf("file content is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:  ChatMessagePartTypeFileURL,
			File:  &MessageInputFile{MessagePartCommon: toolPart.File.MessagePartCommon},
			Extra: toolPart.Extra,
		}, nil
	case ToolPartTypeToolSearchResult:
		if toolPart.ToolSearchResult == nil {
			return MessageInputPart{}, fmt.Errorf("tool search result is nil for tool part type %v", toolPart.Type)
		}
		return MessageInputPart{
			Type:             ChatMessagePartTypeToolSearchResult,
			ToolSearchResult: toolPart.ToolSearchResult,
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
