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

// Package decision defines the Decision component interface for structured
// decision models.
//
// # Overview
//
// A Decision component evaluates a state against a set of typed questions
// and returns answers carrying probabilities and confidence. Unlike a free-form
// [model.ChatModel], a Decider produces constrained, machine-checkable output:
// a choice among named options, a score on an ordered scale, or a noul
// (non-omission uncertainty level) estimate.
//
// All questions in a single [Decider.Decide] call are evaluated against the
// same state in parallel, and each answer is keyed by the question id provided
// by the caller.
//
// # Question Primitives
//
//   - [QuestionTypeChoice]: pick one of the criteria options (1–255 named options)
//   - [QuestionTypeScore]: rate on an ordered scale (2–10 levels)
//   - [QuestionTypeNoul]: estimate a non-omission uncertainty level in [0, 1]
//
// # Implementations
//
// The first implementation is TypeSafe Jev ("System One"), available in the
// sub-package:
//
//	github.com/cloudwego/eino/components/decision/jev
package decision
