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

package team

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConstants(t *testing.T) {
	assert.Equal(t, "team-lead", LeaderAgentName)
	assert.Equal(t, "general-purpose", generalAgentName)
	assert.Equal(t, 30*time.Second, defaultShutdownTimeout)
	assert.Equal(t, 500*time.Millisecond, defaultPollInterval)
}

func TestNopLogger(t *testing.T) {
	l := nopLogger{}
	assert.NotPanics(t, func() {
		l.Printf("should not panic: %d", 42)
	})
}

func TestNopLogger_Printf(t *testing.T) {
	var l Logger = nopLogger{}
	l.Printf("test %s", "val")
}

func TestDefaultLogger(t *testing.T) {
	l := defaultLogger{}
	assert.NotPanics(t, func() {
		l.Printf("test log: %s", "hello")
	})
}

func TestErrTeamNotFound(t *testing.T) {
	assert.NotNil(t, errTeamNotFound)
	assert.Contains(t, errTeamNotFound.Error(), "no active team")
}

func TestInboxMessage_ZeroValue(t *testing.T) {
	var msg InboxMessage
	assert.Equal(t, "", msg.From)
	assert.Equal(t, "", msg.To)
	assert.Equal(t, "", msg.Text)
	assert.Equal(t, "", msg.Summary)
	assert.Equal(t, "", msg.Timestamp)
	assert.False(t, msg.Read)
}

func TestTurnInput_ZeroValue(t *testing.T) {
	var ti TurnInput
	assert.Equal(t, "", ti.TargetAgent)
	assert.Nil(t, ti.Messages)
}

func TestTurnInput_WithValues(t *testing.T) {
	ti := TurnInput{
		TargetAgent: "worker-1",
		Messages:    []string{"hello", "world"},
	}
	assert.Equal(t, "worker-1", ti.TargetAgent)
	assert.Len(t, ti.Messages, 2)
	assert.Equal(t, "hello", ti.Messages[0])
}

func TestInboxMessage_WithValues(t *testing.T) {
	msg := InboxMessage{
		From:      "leader",
		To:        "worker",
		Text:      "do task",
		Summary:   "assignment",
		Timestamp: "2026-01-01T00:00:00Z",
		Read:      true,
	}
	assert.Equal(t, "leader", msg.From)
	assert.Equal(t, "worker", msg.To)
	assert.Equal(t, "do task", msg.Text)
	assert.Equal(t, "assignment", msg.Summary)
	assert.Equal(t, "2026-01-01T00:00:00Z", msg.Timestamp)
	assert.True(t, msg.Read)
}
