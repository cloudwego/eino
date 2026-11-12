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

import "github.com/eino-contrib/jsonschema"

// ResponseFormatType specifies the format the model must output.
type ResponseFormatType string

const (
	ResponseFormatTypeJSONObject ResponseFormatType = "json_object"
	ResponseFormatTypeJSONSchema ResponseFormatType = "json_schema"
)

// ResponseFormat controls the structured output format of the model response.
type ResponseFormat struct {
	Type       ResponseFormatType
	JSONSchema *ResponseFormatJSONSchema
}

// ResponseFormatJSONSchema defines the JSON Schema for structured output.
type ResponseFormatJSONSchema struct {
	Schema *jsonschema.Schema
}
