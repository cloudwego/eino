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
	"sort"
)

type ResponseMetaExtension struct {
	ID                   string               `json:"id,omitempty"`
	Status               ResponseStatus       `json:"status,omitempty"`
	Error                *ResponseError       `json:"error,omitempty"`
	IncompleteDetails    *IncompleteDetails   `json:"incomplete_details,omitempty"`
	PreviousResponseID   string               `json:"previous_response_id,omitempty"`
	Reasoning            *Reasoning           `json:"reasoning,omitempty"`
	ServiceTier          ServiceTier          `json:"service_tier,omitempty"`
	CreatedAt            int64                `json:"created_at,omitempty"`
	PromptCacheRetention PromptCacheRetention `json:"prompt_cache_retention,omitempty"`
}

type AssistantGenTextExtension struct {
	Refusal     *OutputRefusal    `json:"refusal,omitempty"`
	Annotations []*TextAnnotation `json:"annotations,omitempty"`
}

type ResponseError struct {
	Code    ResponseErrorCode `json:"code,omitempty"`
	Message string            `json:"message,omitempty"`
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

	var (
		indices           []int
		indexToAnnotation = map[int]*TextAnnotation{}
	)

	for _, an := range allAnnotations {
		if an == nil {
			continue
		}
		if indexToAnnotation[an.Index] == nil {
			indexToAnnotation[an.Index] = an
			indices = append(indices, an.Index)
		} else {
			return nil, fmt.Errorf("duplicate annotation index %d", an.Index)
		}
	}

	sort.Slice(indices, func(i, j int) bool {
		return indices[i] < indices[j]
	})

	ret.Annotations = make([]*TextAnnotation, 0, len(indices))
	for _, idx := range indices {
		an := *indexToAnnotation[idx]
		an.Index = 0 // clear index
		ret.Annotations = append(ret.Annotations, &an)
	}

	for _, ext := range chunks {
		if ext.Refusal == nil {
			continue
		}
		if ret.Refusal == nil {
			ret.Refusal = ext.Refusal
		} else {
			ret.Refusal.Reason += ext.Refusal.Reason
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
