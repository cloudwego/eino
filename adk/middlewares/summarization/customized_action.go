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

package summarization

import (
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// TypedCustomizedAction is the generic customized action for summarization events.
type TypedCustomizedAction[M adk.MessageType] struct {
	// Type is the action type.
	Type ActionType `json:"type"`

	// Before is set when Type is ActionTypeBeforeSummarize.
	// Emitted after trigger condition is met, before calling model to generate summary.
	Before *TypedBeforeSummarizeAction[M] `json:"before,omitempty"`

	// After is set when Type is ActionTypeAfterSummarize.
	// Emitted after summarization.
	After *TypedAfterSummarizeAction[M] `json:"after,omitempty"`

	// GenerateSummary is set when Type is ActionTypeGenerateSummary.
	// Emitted on each summary generation attempt, including retries and failovers.
	GenerateSummary *TypedGenerateSummaryAction[M] `json:"generate_summary,omitempty"`
}

// CustomizedAction is the default action type using *schema.Message.
type CustomizedAction = TypedCustomizedAction[*schema.Message]

// TypedBeforeSummarizeAction contains the state messages before summarization.
type TypedBeforeSummarizeAction[M adk.MessageType] struct {
	// Messages is the original state messages before summarization.
	Messages []M `json:"messages,omitempty"`
}

// BeforeSummarizeAction is the default type using *schema.Message.
type BeforeSummarizeAction = TypedBeforeSummarizeAction[*schema.Message]

// TypedAfterSummarizeAction contains the state messages after summarization.
type TypedAfterSummarizeAction[M adk.MessageType] struct {
	// Messages is the final state messages after summarization.
	Messages []M `json:"messages,omitempty"`
}

// AfterSummarizeAction is the default type using *schema.Message.
type AfterSummarizeAction = TypedAfterSummarizeAction[*schema.Message]

// GenerateSummaryPhase indicates which phase a model generate attempt belongs to during summarization.
type GenerateSummaryPhase string

const (
	// GenerateSummaryPhasePrimary indicates an attempt using the primary model.
	// Attempt=1 is the initial call; Attempt>1 indicates a retry.
	GenerateSummaryPhasePrimary GenerateSummaryPhase = "primary"

	// GenerateSummaryPhaseFailover indicates an attempt using a failover model
	// after the primary model exhausted all retries or was deemed unrecoverable.
	GenerateSummaryPhaseFailover GenerateSummaryPhase = "failover"
)

// TypedGenerateSummaryAction contains details of a single model generate attempt during summarization.
// Emitted on every attempt, whether it succeeds or fails.
type TypedGenerateSummaryAction[M adk.MessageType] struct {
	// Attempt is the 1-based attempt number within the current phase.
	// For primary phase, Attempt=1 is the initial call and Attempt>1 indicates retries.
	// For failover phase, Attempt counts the failover rounds (1, 2, 3, ...).
	Attempt int `json:"attempt"`

	// Phase indicates which phase this generate attempt belongs to.
	Phase GenerateSummaryPhase `json:"phase"`

	// ModelResponse is the raw response returned by the model.
	// It may be nil when the model call fails without returning a response.
	ModelResponse M `json:"model_response,omitempty"`

	// err is the error returned by the model call, if any. Use GetError to access it.
	err error
}

// GenerateSummaryAction is the default type using *schema.Message.
type GenerateSummaryAction = TypedGenerateSummaryAction[*schema.Message]

// GetError returns the error from the model call, if any.
func (a *TypedGenerateSummaryAction[M]) GetError() error {
	return a.err
}
