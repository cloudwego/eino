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
	Register[Message]()
	Register[Document]()
	Register[RoleType]()
	Register[ToolCall]()
	Register[FunctionCall]()
	Register[ResponseMeta]()
	Register[TokenUsage]()
	Register[LogProbs]()
	Register[ChatMessagePart]()
	Register[ChatMessagePartType]()
	Register[ChatMessageImageURL]()
	Register[ChatMessageAudioURL]()
	Register[ChatMessageVideoURL]()
	Register[ChatMessageFileURL]()
	Register[ImageURLDetail]()
	Register[PromptTokenDetails]()
}

func RegisterName[T any](name string) {
	err := serialization.GenericRegister[T](name)
	if err != nil {
		panic(err)
	}

	gob.RegisterName(name, generic.NewInstance[T]())
}

func Register[T any]() {
	value := generic.NewInstance[T]()

	gob.Register(value)

	// Default to printed representation for unnamed types
	rt := reflect.TypeOf(value)
	name := rt.String()

	// But for named types (or pointers to them), qualify with import path (but see inner comment).
	// Dereference one pointer looking for a named type.
	star := ""
	if rt.Name() == "" {
		if pt := rt; pt.Kind() == reflect.Pointer {
			star = "*"
			// NOTE: The following line should be rt = pt.Elem() to implement
			// what the comment above claims, but fixing it would break compatibility
			// with existing gobs.
			//
			// Given package p imported as "full/p" with these definitions:
			//     package p
			//     type T1 struct { ... }
			// this table shows the intended and actual strings used by gob to
			// name the types:
			//
			// Type      Correct string     Actual string
			//
			// T1        full/p.T1          full/p.T1
			// *T1       *full/p.T1         *p.T1
			//
			// The missing full path cannot be fixed without breaking existing gob decoders.
			rt = pt
		}
	}
	if rt.Name() != "" {
		if rt.PkgPath() == "" {
			name = star + rt.Name()
		} else {
			name = star + rt.PkgPath() + "." + rt.Name()
		}
	}

	err := serialization.GenericRegister[T](name)
	if err != nil {
		panic(err)
	}
}
