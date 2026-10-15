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

package decision

import (
	"context"
	"errors"
	"fmt"
)

// Decider is the interface of the decision component.
//
// Decide evaluates all questions against the same state in parallel and
// returns one answer per question id. Question ids are local identifiers and
// are not exposed to the underlying model.
//
//go:generate mockgen -destination ../../internal/mock/components/decision/decider_mock.go --package decision -source interface.go
type Decider interface {
	Decide(ctx context.Context, state any, questions map[string]Question, opts ...Option) (map[string]*Answer, error) // invoke
}

// QuestionType is the type of a question primitive.
type QuestionType string

const (
	// QuestionTypeChoice is a question that picks one option out of named criteria.
	QuestionTypeChoice QuestionType = "choice"
	// QuestionTypeScore is a question that rates the state on an ordered scale.
	QuestionTypeScore QuestionType = "score"
	// QuestionTypeNoul is a question that estimates a non-omission uncertainty level.
	QuestionTypeNoul QuestionType = "noul"
)

const (
	maxChoiceCriteria = 255
	minScoreLevels    = 2
	maxScoreLevels    = 10
)

// Question is a typed question evaluated against a state.
//
// The shape of Criteria depends on Type:
//   - QuestionTypeChoice: map[string]string mapping option name to its description (1–255 entries)
//   - QuestionTypeScore: []string of level descriptions, from the lowest to the highest level (2–10 entries)
//   - QuestionTypeNoul: optional map[string]string with "true"/"false" keys describing each outcome
//
// Use [NewChoiceQuestion], [NewScoreQuestion] and [NewNoulQuestion] to build
// validated questions.
type Question struct {
	// Type is the question primitive type.
	Type QuestionType
	// Instructions tells the model what to evaluate.
	Instructions string
	// Criteria describes the answer space, see the type doc for its shape per Type.
	Criteria any
}

// NewChoiceQuestion creates a choice question.
// criteria maps each option name to its description and must hold 1–255 entries.
func NewChoiceQuestion(instructions string, criteria map[string]string) (Question, error) {
	if instructions == "" {
		return Question{}, errors.New("decision: choice question requires non-empty instructions")
	}
	if len(criteria) == 0 || len(criteria) > maxChoiceCriteria {
		return Question{}, fmt.Errorf("decision: choice question requires 1-%d criteria, got %d", maxChoiceCriteria, len(criteria))
	}
	return Question{
		Type:         QuestionTypeChoice,
		Instructions: instructions,
		Criteria:     criteria,
	}, nil
}

// NewScoreQuestion creates a score question.
// levels describes each scale level from lowest to highest and must hold 2–10 entries.
func NewScoreQuestion(instructions string, levels []string) (Question, error) {
	if instructions == "" {
		return Question{}, errors.New("decision: score question requires non-empty instructions")
	}
	if len(levels) < minScoreLevels || len(levels) > maxScoreLevels {
		return Question{}, fmt.Errorf("decision: score question requires %d-%d levels, got %d", minScoreLevels, maxScoreLevels, len(levels))
	}
	return Question{
		Type:         QuestionTypeScore,
		Instructions: instructions,
		Criteria:     levels,
	}, nil
}

// NewNoulQuestion creates a noul question without explicit criteria.
func NewNoulQuestion(instructions string) Question {
	return Question{
		Type:         QuestionTypeNoul,
		Instructions: instructions,
	}
}

// Answer is the answer to a single [Question].
//
// The populated fields depend on Type:
//   - QuestionTypeChoice: Choice, Probabilities (per option), Confidence
//   - QuestionTypeScore: Score, Probabilities (keyed by level index as string), Confidence, Legend
//   - QuestionTypeNoul: Noul (no Confidence)
type Answer struct {
	// Type is the question primitive type this answer belongs to.
	Type QuestionType
	// Choice is the chosen option name, for QuestionTypeChoice.
	Choice string
	// Score is the resulting score, for QuestionTypeScore.
	Score float64
	// Noul is the non-omission uncertainty level, for QuestionTypeNoul.
	Noul float64
	// Probabilities is the probability distribution over the answer space.
	// For score answers the keys are the level indices in string form.
	Probabilities map[string]float64
	// Confidence is the model's confidence in the answer. It is nil when the
	// answer carries no confidence (e.g. noul answers).
	Confidence *float64
	// Legend describes the score levels the Probabilities indices refer to, for QuestionTypeScore.
	Legend []string
}

// IsConfident reports whether the answer carries a confidence greater than or
// equal to threshold. It returns false when Confidence is nil.
func (a *Answer) IsConfident(threshold float64) bool {
	return a.Confidence != nil && *a.Confidence >= threshold
}

// NodeInput is the self-contained input of a decision node in a graph or chain.
type NodeInput struct {
	// State is the state evaluated by the decider.
	State any
	// Questions are the questions to evaluate, keyed by question id.
	Questions map[string]Question
}
