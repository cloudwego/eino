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

type TextCitation struct {
	Type TextCitationType `json:"type"`

	CharLocation            *CitationCharLocation            `json:"char_location,omitempty"`
	PageLocation            *CitationPageLocation            `json:"page_location,omitempty"`
	ContentBlockLocation    *CitationContentBlockLocation    `json:"content_block_location,omitempty"`
	WebSearchResultLocation *CitationWebSearchResultLocation `json:"web_search_result_location,omitempty"`
}

type CitationCharLocation struct {
	CitedText string `json:"cited_text"`

	DocumentTitle string `json:"document_title"`
	DocumentIndex int64  `json:"document_index"`

	StartCharIndex int64 `json:"start_char_index"`
	EndCharIndex   int64 `json:"end_char_index"`
}

type CitationPageLocation struct {
	CitedText string `json:"cited_text"`

	DocumentTitle string `json:"document_title"`
	DocumentIndex int64  `json:"document_index"`

	StartPageNumber int64 `json:"start_page_number"`
	EndPageNumber   int64 `json:"end_page_number"`
}

type CitationContentBlockLocation struct {
	CitedText string `json:"cited_text"`

	DocumentTitle string `json:"document_title"`
	DocumentIndex int64  `json:"document_index"`

	StartBlockIndex int64 `json:"start_block_index"`
	EndBlockIndex   int64 `json:"end_block_index"`
}

type CitationWebSearchResultLocation struct {
	CitedText string `json:"cited_text"`

	Title string `json:"title"`
	URL   string `json:"url"`

	EncryptedIndex string `json:"encrypted_index"`
}
