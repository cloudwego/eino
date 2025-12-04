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

// Package document defines callback payloads used by document loaders.
package document

import (
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// LoaderCallbackInput is the input for the loader callback.
type LoaderCallbackInput struct {
	// Source is the source of the documents.
	Source Source

	// Extra is the extra information for the callback.
	Extra map[string]any
}

// LoaderCallbackOutput is the output for the loader callback.
type LoaderCallbackOutput struct {
	// Source is the source of the documents.
	Source Source

	// Docs is the documents to be loaded.
	Docs []*schema.Document

	// Extra is the extra information for the callback.
	Extra map[string]any
}

// ConvLoaderCallbackInput converts the callback input to the loader callback input.
func ConvLoaderCallbackInput(src callbacks.CallbackInput) *LoaderCallbackInput {
	switch t := src.(type) {
	case *LoaderCallbackInput:
		return t
	case Source:
		return &LoaderCallbackInput{
			Source: t,
		}
	default:
		return nil
	}
}

// ConvLoaderCallbackOutput converts the callback output to the loader callback output.
func ConvLoaderCallbackOutput(src callbacks.CallbackOutput) *LoaderCallbackOutput {
	switch t := src.(type) {
	case *LoaderCallbackOutput:
		return t
	case []*schema.Document:
		return &LoaderCallbackOutput{
			Docs: t,
		}
	default:
		return nil
	}
}
