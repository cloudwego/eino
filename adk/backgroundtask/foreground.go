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

package backgroundtask

import "github.com/cloudwego/eino/adk/internal/foreground"

// ForegroundCandidate describes a foreground execution that may be handed off
// to background ownership. It is supplied to the ShouldAutoBackground policy
// callbacks when a foreground observation timer expires. TaskID is
// pre-allocated for correlation; no Task exists until a handoff succeeds.
type ForegroundCandidate = foreground.CandidateInfo

// DefaultForegroundTimeoutMs is the foreground observation timeout applied
// when a configuration leaves it unset.
const DefaultForegroundTimeoutMs = foreground.DefaultTimeoutMs
