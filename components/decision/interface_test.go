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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewChoiceQuestion(t *testing.T) {
	q, err := NewChoiceQuestion("pick a route", map[string]string{
		"billing":  "questions about billing",
		"shipping": "questions about shipping",
	})
	assert.NoError(t, err)
	assert.Equal(t, QuestionTypeChoice, q.Type)
	assert.Equal(t, "pick a route", q.Instructions)

	_, err = NewChoiceQuestion("", map[string]string{"a": "b"})
	assert.Error(t, err)

	_, err = NewChoiceQuestion("pick", nil)
	assert.Error(t, err)

	tooMany := make(map[string]string, 256)
	for i := 0; i < 256; i++ {
		tooMany[string(rune('a'+i%26))+string(rune('a'+i/26))] = "d"
	}
	_, err = NewChoiceQuestion("pick", tooMany)
	assert.Error(t, err)
}

func TestNewScoreQuestion(t *testing.T) {
	q, err := NewScoreQuestion("rate the urgency", []string{"low", "medium", "high"})
	assert.NoError(t, err)
	assert.Equal(t, QuestionTypeScore, q.Type)

	_, err = NewScoreQuestion("", []string{"low", "high"})
	assert.Error(t, err)

	_, err = NewScoreQuestion("rate", []string{"only"})
	assert.Error(t, err)

	_, err = NewScoreQuestion("rate", make([]string, 11))
	assert.Error(t, err)
}

func TestNewNoulQuestion(t *testing.T) {
	q := NewNoulQuestion("how uncertain is this")
	assert.Equal(t, QuestionTypeNoul, q.Type)
	assert.Nil(t, q.Criteria)
}

func TestAnswerIsConfident(t *testing.T) {
	conf := 0.9
	a := &Answer{Type: QuestionTypeChoice, Confidence: &conf}
	assert.True(t, a.IsConfident(0.9))
	assert.True(t, a.IsConfident(0.5))
	assert.False(t, a.IsConfident(0.99))

	a = &Answer{Type: QuestionTypeNoul}
	assert.False(t, a.IsConfident(0))
}
