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

package schema

import (
	"encoding/gob"
	"reflect"

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/internal/serialization"
)

func init() {
	RegisterName[Message]("_eino_message")
	RegisterName[Document]("_eino_document")
	RegisterName[RoleType]("_eino_role_type")
	RegisterName[ToolCall]("_eino_tool_call")
	RegisterName[FunctionCall]("_eino_function_call")
	RegisterName[ResponseMeta]("_eino_response_meta")
	RegisterName[TokenUsage]("_eino_token_usage")
	RegisterName[LogProbs]("_eino_log_probs")
	RegisterName[ChatMessagePart]("_eino_chat_message_part")
	RegisterName[ChatMessagePartType]("_eino_chat_message_type")
	RegisterName[ChatMessageImageURL]("_eino_chat_message_image_url")
	RegisterName[ChatMessageAudioURL]("_eino_chat_message_audio_url")
	RegisterName[ChatMessageVideoURL]("_eino_chat_message_video_url")
	RegisterName[ChatMessageFileURL]("_eino_chat_message_file_url")
	RegisterName[MessageInputPart]("_eino_message_input_part")
	RegisterName[MessageInputImage]("_eino_message_input_image")
	RegisterName[MessageInputAudio]("_eino_message_input_audio")
	RegisterName[MessageInputVideo]("_eino_message_input_video")
	RegisterName[MessageInputFile]("_eino_message_input_file")
	RegisterName[MessageOutputPart]("_eino_message_output_part")
	RegisterName[MessageOutputImage]("_eino_message_output_image")
	RegisterName[MessageOutputAudio]("_eino_message_output_audio")
	RegisterName[MessageOutputVideo]("_eino_message_output_video")
	RegisterName[MessagePartCommon]("_eino_message_part_common")
	RegisterName[ImageURLDetail]("_eino_image_url_detail")
	RegisterName[PromptTokenDetails]("_eino_prompt_token_details")
}

// RegisterName registers a type with a specific name for serialization. This is
// required for any type you intend to persist in a graph or ADK checkpoint.
// Use this function to maintain backward compatibility by mapping a type to a
// previously used name. For new types, `Register` is preferred.
//
// It is recommended to call this in an `init()` function in the file where the
// type is declared.
//
// What to Register:
//   - Top-level types used as state (e.g., structs).
//   - Concrete types that are assigned to interface fields.
//
// What NOT to Register:
//   - Struct fields with concrete types (e.g., `string`, `int`, other structs).
//     These are inferred via reflection.
//
// Serialization Rules:
//
// The serialization behavior is based on Go's standard `encoding/gob` package.
// See https://pkg.go.dev/encoding/gob for detailed rules.
//   - Only exported struct fields are serialized.
//   - Functions and channels are not supported and will be ignored.
//
// This function panics if registration fails.
func RegisterName[T any](name string) {
	gob.RegisterName(name, generic.NewInstance[T]())

	err := serialization.GenericRegister[T](name)
	if err != nil {
		panic(err)
	}
}

func getTypeName(rt reflect.Type) string {
	name := rt.String()

	// But for named types (or pointers to them), qualify with import path.
	// Dereference one pointer looking for a named type.
	star := ""
	if rt.Name() == "" {
		if pt := rt; pt.Kind() == reflect.Pointer {
			star = "*"
			rt = pt.Elem()
		}
	}
	if rt.Name() != "" {
		if rt.PkgPath() == "" {
			name = star + rt.Name()
		} else {
			name = star + rt.PkgPath() + "." + rt.Name()
		}
	}
	return name
}

// Register registers a type for serialization. This is required for any type
// you intend to persist in a graph or ADK checkpoint. It automatically determines
// the type name and is the recommended method for registering new types.
//
// It is recommended to call this in an `init()` function in the file where the
// type is declared.
//
// What to Register:
//   - Top-level types used as state (e.g., structs).
//   - Concrete types that are assigned to interface fields.
//
// What NOT to Register:
//   - Struct fields with concrete types (e.g., `string`, `int`, other structs).
//     These are inferred via reflection.
//
// Serialization Rules:
//
// The serialization behavior is based on Go's standard `encoding/gob` package.
// See https://pkg.go.dev/encoding/gob for detailed rules.
//   - Only exported struct fields are serialized.
//   - Functions and channels are not supported and will be ignored.
//
// This function panics if registration fails.
func Register[T any]() {
	value := generic.NewInstance[T]()

	gob.Register(value)

	name := getTypeName(reflect.TypeOf(value))

	err := serialization.GenericRegister[T](name)
	if err != nil {
		panic(err)
	}
}
