package team

import (
	"strings"
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

func TestValidateName_EmptyName(t *testing.T) {
	err := validateName("", "agent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must not be empty")
}

func TestValidateName_TooLong(t *testing.T) {
	longName := strings.Repeat("a", 129)
	err := validateName(longName, "agent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "must not exceed 128 characters")
}

func TestValidateName_ExactlyMaxLength(t *testing.T) {
	name := strings.Repeat("a", 128)
	err := validateName(name, "agent")
	assert.NoError(t, err)
}

func TestValidateName_InvalidChars(t *testing.T) {
	cases := []string{
		"has space",
		"has/slash",
		"../traversal",
		"-starts-with-dash",
		".starts-with-dot",
		"_starts-with-underscore",
		"name@invalid",
		"name!bang",
		"name#hash",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateName(name, "team")
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "invalid characters")
		})
	}
}

func TestValidateName_ReservedName(t *testing.T) {
	err := validateName("*", "agent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid characters")
}

func TestValidateName_ValidNames(t *testing.T) {
	cases := []string{
		"a",
		"agent1",
		"my-agent",
		"my.agent",
		"my_agent",
		"Agent123",
		"A.b-c_d",
		"team-lead",
		"9lives",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateName(name, "agent")
			assert.NoError(t, err)
		})
	}
}

func TestValidateName_KindAppearsInError(t *testing.T) {
	err := validateName("", "custom-kind")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "custom-kind")
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

func TestValidNameRegex(t *testing.T) {
	assert.True(t, validNameRegex.MatchString("abc"))
	assert.True(t, validNameRegex.MatchString("a1"))
	assert.False(t, validNameRegex.MatchString(""))
	assert.False(t, validNameRegex.MatchString("-abc"))
	assert.False(t, validNameRegex.MatchString(".abc"))
}

func TestReservedNames(t *testing.T) {
	assert.True(t, reservedNames["*"])
	assert.False(t, reservedNames["team-lead"])
	assert.False(t, reservedNames["anything-else"])
}
