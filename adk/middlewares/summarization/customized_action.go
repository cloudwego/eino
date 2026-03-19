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
)

type CustomizedAction struct {
	// Type is the action type.
	Type ActionType `json:"type"`

	// Before is set when Type is ActionTypeBeforeSummarize.
	// Emitted after trigger condition is met, before calling model to generate summary.
	Before *BeforeSummarizeAction `json:"before,omitempty"`

	// After is set when Type is ActionTypeAfterSummarize.
	// Emitted after summarization.
	After *AfterSummarizeAction `json:"after,omitempty"`

	// GenerateSummary is set when Type is ActionTypeGenerateSummary.
	// Emitted on each summary generation attempt, including retries and failovers.
	GenerateSummary *GenerateSummaryAction `json:"generate_summary,omitempty"`
}

type BeforeSummarizeAction struct {
	// Messages is the original state messages before summarization.
	Messages []adk.Message `json:"messages,omitempty"`
}

type AfterSummarizeAction struct {
	// Messages is the final state messages after summarization.
	Messages []adk.Message `json:"messages,omitempty"`
}

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

// GenerateSummaryAction contains details of a single model generate attempt during summarization.
// Emitted on every attempt, whether it succeeds or fails.
type GenerateSummaryAction struct {
	// Attempt is the 1-based attempt number within the current phase.
	// For primary phase, Attempt=1 is the initial call and Attempt>1 indicates retries.
	// For failover phase, Attempt counts the failover rounds (1, 2, 3, ...).
	Attempt int `json:"attempt"`

	// Phase indicates which phase this generate attempt belongs to.
	Phase GenerateSummaryPhase `json:"phase"`

	// ModelResponse is the raw response returned by the model.
	// It may be nil when the model call fails without returning a response.
	ModelResponse adk.Message `json:"model_response,omitempty"`

	// err is the error returned by the model call, if any. Use GetError to access it.
	err error
}

// GetError returns the error from the model call, if any.
func (a *GenerateSummaryAction) GetError() error {
	return a.err
}
