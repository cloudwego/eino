package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentToolName(t *testing.T) {
	assert.Equal(t, "Agent", agentToolName)
}

func TestSendMessageToolName(t *testing.T) {
	assert.Equal(t, "SendMessage", sendMessageToolName)
}

func TestTeamCreateToolName(t *testing.T) {
	assert.Equal(t, "TeamCreate", teamCreateToolName)
}

func TestTeamDeleteToolName(t *testing.T) {
	assert.Equal(t, "TeamDelete", teamDeleteToolName)
}

func TestAgentToolDesc_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, agentToolDesc)
}

func TestAgentToolDescChinese_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, agentToolDescChinese)
}

func TestSendMessageToolDesc_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, sendMessageToolDesc)
}

func TestSendMessageToolDescChinese_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, sendMessageToolDescChinese)
}

func TestTeamCreateToolDesc_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, teamCreateToolDesc)
}

func TestTeamCreateToolDescChinese_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, teamCreateToolDescChinese)
}

func TestTeamDeleteToolDesc_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, teamDeleteToolDesc)
}

func TestTeamDeleteToolDescChinese_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, teamDeleteToolDescChinese)
}
