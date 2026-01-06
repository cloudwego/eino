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

package claude

import (
	"fmt"
)

type ResponseMetaExtension struct {
	ID         string `json:"id,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`
}

type AssistantGenTextExtension struct {
	Citations []*TextCitation `json:"citations,omitempty"`
}

type TextCitation struct {
	Type TextCitationType `json:"type,omitempty"`

	CharLocation            *CitationCharLocation            `json:"char_location,omitempty"`
	PageLocation            *CitationPageLocation            `json:"page_location,omitempty"`
	ContentBlockLocation    *CitationContentBlockLocation    `json:"content_block_location,omitempty"`
	WebSearchResultLocation *CitationWebSearchResultLocation `json:"web_search_result_location,omitempty"`
}

type CitationCharLocation struct {
	CitedText string `json:"cited_text,omitempty"`

	DocumentTitle string `json:"document_title,omitempty"`
	DocumentIndex int    `json:"document_index,omitempty"`

	StartCharIndex int `json:"start_char_index,omitempty"`
	EndCharIndex   int `json:"end_char_index,omitempty"`
}

type CitationPageLocation struct {
	CitedText string `json:"cited_text,omitempty"`

	DocumentTitle string `json:"document_title,omitempty"`
	DocumentIndex int    `json:"document_index,omitempty"`

	StartPageNumber int `json:"start_page_number,omitempty"`
	EndPageNumber   int `json:"end_page_number,omitempty"`
}

type CitationContentBlockLocation struct {
	CitedText string `json:"cited_text,omitempty"`

	DocumentTitle string `json:"document_title,omitempty"`
	DocumentIndex int    `json:"document_index,omitempty"`

	StartBlockIndex int `json:"start_block_index,omitempty"`
	EndBlockIndex   int `json:"end_block_index,omitempty"`
}

type CitationWebSearchResultLocation struct {
	CitedText string `json:"cited_text,omitempty"`

	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`

	EncryptedIndex string `json:"encrypted_index,omitempty"`
}

func ConcatAssistantGenTextExtensions(chunks []*AssistantGenTextExtension) (*AssistantGenTextExtension, error) {
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no assistant generated text extension found")
	}
	if len(chunks) == 1 {
		return chunks[0], nil
	}

	ret := &AssistantGenTextExtension{
		Citations: make([]*TextCitation, 0, len(chunks)),
	}

	for _, ext := range chunks {
		ret.Citations = append(ret.Citations, ext.Citations...)
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
		if ext.StopReason != "" {
			ret.StopReason = ext.StopReason
		}
	}

	return ret, nil
}
