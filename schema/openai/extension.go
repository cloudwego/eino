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

package openai

import (
	"fmt"
)

type ResponseMetaExtension struct {
	ID                 string             `json:"id,omitempty"`
	Status             ResponseStatus     `json:"status,omitempty"`
	Error              *ResponseError     `json:"error,omitempty"`
	IncompleteDetails  *IncompleteDetails `json:"incomplete_details,omitempty"`
	PreviousResponseID string             `json:"previous_response_id,omitempty"`
	Reasoning          *Reasoning         `json:"reasoning,omitempty"`
	ServiceTier        ServiceTier        `json:"service_tier,omitempty"`

	StreamingError *StreamingResponseError `json:"streaming_error,omitempty"`
}

type AssistantGenTextExtension struct {
	Refusal     *OutputRefusal    `json:"refusal,omitempty"`
	Annotations []*TextAnnotation `json:"annotations,omitempty"`
}

type ResponseError struct {
	Code    ResponseErrorCode `json:"code,omitempty"`
	Message string            `json:"message,omitempty"`
}

type StreamingResponseError struct {
	Code    ResponseErrorCode
	Message string
	Param   string
}

type IncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

type Reasoning struct {
	Effort  ReasoningEffort  `json:"effort,omitempty"`
	Summary ReasoningSummary `json:"summary,omitempty"`
}

type OutputRefusal struct {
	Reason string `json:"reason,omitempty"`
}

type TextAnnotation struct {
	Index int `json:"index,omitempty"`

	Type TextAnnotationType `json:"type,omitempty"`

	FileCitation          *TextAnnotationFileCitation          `json:"file_citation,omitempty"`
	URLCitation           *TextAnnotationURLCitation           `json:"url_citation,omitempty"`
	ContainerFileCitation *TextAnnotationContainerFileCitation `json:"container_file_citation,omitempty"`
	FilePath              *TextAnnotationFilePath              `json:"file_path,omitempty"`
}

type TextAnnotationFileCitation struct {
	// The ID of the file.
	FileID string `json:"file_id,omitempty"`
	// The filename of the file cited.
	Filename string `json:"filename,omitempty"`

	// The index of the file in the list of files.
	Index int `json:"index,omitempty"`
}

type TextAnnotationURLCitation struct {
	// The title of the web resource.
	Title string `json:"title,omitempty"`
	// The URL of the web resource.
	URL string `json:"url,omitempty"`

	// The index of the first character of the URL citation in the message.
	StartIndex int `json:"start_index,omitempty"`
	// The index of the last character of the URL citation in the message.
	EndIndex int `json:"end_index,omitempty"`
}

type TextAnnotationContainerFileCitation struct {
	// The ID of the container file.
	ContainerID string `json:"container_id,omitempty"`

	// The ID of the file.
	FileID string `json:"file_id,omitempty"`
	// The filename of the container file cited.
	Filename string `json:"filename,omitempty"`

	// The index of the first character of the container file citation in the message.
	StartIndex int `json:"start_index,omitempty"`
	// The index of the last character of the container file citation in the message.
	EndIndex int `json:"end_index,omitempty"`
}

type TextAnnotationFilePath struct {
	// The ID of the file.
	FileID string `json:"file_id,omitempty"`

	// The index of the file in the list of files.
	Index int `json:"index,omitempty"`
}

func ConcatAssistantGenTextExtensions(chunks []*AssistantGenTextExtension) (*AssistantGenTextExtension, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no assistant generated text extension found")
	}

	ret := &AssistantGenTextExtension{}

	var allAnnotations []*TextAnnotation
	for _, ext := range chunks {
		allAnnotations = append(allAnnotations, ext.Annotations...)
	}

	var annotationArray []*TextAnnotation
	for _, an := range allAnnotations {
		if an == nil {
			continue
		}

		annotationArray = expandSlice(an.Index, annotationArray)
		if annotationArray[an.Index] == nil {
			annotationArray[an.Index] = an
		} else {
			return nil, fmt.Errorf("duplicate annotation index %d", an.Index)
		}
	}

	ret.Annotations = make([]*TextAnnotation, 0, len(annotationArray))
	for _, an := range annotationArray {
		if an != nil {
			ret.Annotations = append(ret.Annotations, an)
		}
	}

	return ret, nil
}

func ConcatResponseMetaExtensions(chunks []*ResponseMetaExtension) (*ResponseMetaExtension, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no response meta extension found")
	}
	if len(chunks) == 1 {
		return chunks[0], nil
	}

	ret := &ResponseMetaExtension{}

	for _, ext := range chunks {
		if ext.ID != "" {
			ret.ID = ext.ID
		}
		if ext.Status != "" {
			ret.Status = ext.Status
		}
		if ext.Error != nil {
			ret.Error = ext.Error
		}
		if ext.StreamingError != nil {
			ret.StreamingError = ext.StreamingError
		}
		if ext.IncompleteDetails != nil {
			ret.IncompleteDetails = ext.IncompleteDetails
		}
		if ext.PreviousResponseID != "" {
			ret.PreviousResponseID = ext.PreviousResponseID
		}
		if ext.Reasoning != nil {
			ret.Reasoning = ext.Reasoning
		}
		if ext.ServiceTier != "" {
			ret.ServiceTier = ext.ServiceTier
		}
	}

	return ret, nil
}

func expandSlice[T any](idx int, s []T) []T {
	if len(s) > idx {
		return s
	}
	return append(s, make([]T, idx-len(s)+1)...)
}
