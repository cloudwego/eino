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

package gemini

import (
	"fmt"
)

type ResponseMetaExtension struct {
	ID            string             `json:"id,omitempty"`
	FinishReason  string             `json:"finish_reason,omitempty"`
	GroundingMeta *GroundingMetadata `json:"grounding_meta,omitempty"`
}

type GroundingMetadata struct {
	// List of supporting references retrieved from specified grounding source.
	GroundingChunks []*GroundingChunk `json:"grounding_chunks,omitempty"`
	// Optional. List of grounding support.
	GroundingSupports []*GroundingSupport `json:"grounding_supports,omitempty"`
	// Optional. Google search entry for the following-up web searches.
	SearchEntryPoint *SearchEntryPoint `json:"search_entry_point,omitempty"`
	// Optional. Web search queries for the following-up web search.
	WebSearchQueries []string `json:"web_search_queries,omitempty"`
}

type GroundingChunk struct {
	// Grounding chunk from the web.
	Web *GroundingChunkWeb `json:"web,omitempty"`
}

// Chunk from the web.
type GroundingChunkWeb struct {
	// Domain of the (original) URI. This field is not supported in Gemini API.
	Domain string `json:"domain,omitempty"`
	// Title of the chunk.
	Title string `json:"title,omitempty"`
	// URI reference of the chunk.
	URI string `json:"uri,omitempty"`
}

type GroundingSupport struct {
	// Confidence score of the support references. Ranges from 0 to 1. 1 is the most confident.
	// For Gemini 2.0 and before, this list must have the same size as the grounding_chunk_indices.
	// For Gemini 2.5 and after, this list will be empty and should be ignored.
	ConfidenceScores []float32 `json:"confidence_scores,omitempty"`
	// A list of indices (into 'grounding_chunk') specifying the citations associated with
	// the claim. For instance [1,3,4] means that grounding_chunk[1], grounding_chunk[3],
	// grounding_chunk[4] are the retrieved content attributed to the claim.
	GroundingChunkIndices []int `json:"grounding_chunk_indices,omitempty"`
	// Segment of the content this support belongs to.
	Segment *Segment `json:"segment,omitempty"`
}

// Segment of the content.
type Segment struct {
	// Output only. End index in the given Part, measured in bytes. Offset from the start
	// of the Part, exclusive, starting at zero.
	EndIndex int `json:"end_index,omitempty"`
	// Output only. The index of a Part object within its parent Content object.
	PartIndex int `json:"part_index,omitempty"`
	// Output only. Start index in the given Part, measured in bytes. Offset from the start
	// of the Part, inclusive, starting at zero.
	StartIndex int `json:"start_index,omitempty"`
	// Output only. The text corresponding to the segment from the response.
	Text string `json:"text,omitempty"`
}

// Google search entry point.
type SearchEntryPoint struct {
	// Optional. Web content snippet that can be embedded in a web page or an app webview.
	RenderedContent string `json:"rendered_content,omitempty"`
	// Optional. Base64 encoded JSON representing array of tuple.
	SDKBlob []byte `json:"sdk_blob,omitempty"`
}

// ConcatResponseMetaExtensions concatenates multiple ResponseMetaExtension chunks into a single one.
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
		if ext.FinishReason != "" {
			ret.FinishReason = ext.FinishReason
		}
		if ext.GroundingMeta != nil {
			ret.GroundingMeta = ext.GroundingMeta
		}
	}

	return ret, nil
}
