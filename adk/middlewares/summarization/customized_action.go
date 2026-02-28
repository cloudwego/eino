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

	// Before is set when Type is ActionTypeBeforeSummary.
	// Emitted after trigger condition is met, before calling model to generate summary.
	Before *BeforeSummaryAction `json:"before,omitempty"`

	// After is set when Type is ActionTypeAfterSummary.
	// Emitted after summarization.
	After *AfterSummaryAction `json:"after,omitempty"`
}

type BeforeSummaryAction struct {
	// Messages is the original state messages before summarization.
	Messages []adk.Message `json:"messages,omitempty"`
}

type AfterSummaryAction struct {
	// Messages is the final state messages after summarization.
	Messages []adk.Message `json:"messages,omitempty"`
}
