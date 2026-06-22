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
