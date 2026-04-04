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
	"context"
	"path/filepath"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

func TestNewTaskAssignedNotifier_EmptyTeamName_ReturnsNil(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	teamNameFn := func() string { return "" }
	notifier := newTaskAssignedNotifier(conf, teamNameFn)

	err := notifier(context.Background(), plantask.TaskAssignment{
		TaskID:      "1",
		Subject:     "test",
		Description: "desc",
		Owner:       "worker",
		AssignedBy:  "team-lead",
	})
	assert.NoError(t, err)

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	_, ok := backend.files[inboxPath]
	assert.False(t, ok)
}

func TestNewTaskAssignedNotifier_ValidTeamName_SendsMessage(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	ctx := context.Background()
	teamName := "myteam"

	_, err := conf.CreateTeam(ctx, teamName, "desc", LeaderAgentName, "general-purpose")
	assert.NoError(t, err)

	teamNameFn := func() string { return teamName }
	notifier := newTaskAssignedNotifier(conf, teamNameFn)

	err = notifier(ctx, plantask.TaskAssignment{
		TaskID:      "1",
		Subject:     "test task",
		Description: "task description",
		Owner:       "worker",
		AssignedBy:  "team-lead",
	})
	assert.NoError(t, err)

	inboxPath := inboxFilePath("/tmp/test", teamName, "worker")
	backend.mu.RLock()
	content, ok := backend.files[inboxPath]
	backend.mu.RUnlock()
	assert.True(t, ok)

	var msgs []InboxMessage
	err = sonic.UnmarshalString(content, &msgs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "team-lead", msgs[0].From)
	assert.Equal(t, "worker", msgs[0].To)
	assert.Contains(t, msgs[0].Text, "task_assignment")
	assert.Contains(t, msgs[0].Text, "test task")
}

func TestNewTaskAssignedNotifier_EmptyAssignedBy_DefaultsToLeader(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	ctx := context.Background()
	teamName := "myteam"

	_, err := conf.CreateTeam(ctx, teamName, "desc", LeaderAgentName, "general-purpose")
	assert.NoError(t, err)

	teamNameFn := func() string { return teamName }
	notifier := newTaskAssignedNotifier(conf, teamNameFn)

	err = notifier(ctx, plantask.TaskAssignment{
		TaskID:      "2",
		Subject:     "another task",
		Description: "desc",
		Owner:       "worker",
		AssignedBy:  "",
	})
	assert.NoError(t, err)

	inboxPath := inboxFilePath("/tmp/test", teamName, "worker")
	backend.mu.RLock()
	content := backend.files[inboxPath]
	backend.mu.RUnlock()

	var msgs []InboxMessage
	err = sonic.UnmarshalString(content, &msgs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, LeaderAgentName, msgs[0].From)
}

func TestNewTaskAssignedNotifier_PayloadContainsCorrectFields(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	ctx := context.Background()
	teamName := "myteam"

	_, err := conf.CreateTeam(ctx, teamName, "desc", LeaderAgentName, "general-purpose")
	assert.NoError(t, err)

	teamNameFn := func() string { return teamName }
	notifier := newTaskAssignedNotifier(conf, teamNameFn)

	err = notifier(ctx, plantask.TaskAssignment{
		TaskID:      "42",
		Subject:     "fix bug",
		Description: "fix the login bug",
		Owner:       "dev1",
		AssignedBy:  "team-lead",
	})
	assert.NoError(t, err)

	inboxPath := inboxFilePath("/tmp/test", teamName, "dev1")
	backend.mu.RLock()
	content := backend.files[inboxPath]
	backend.mu.RUnlock()

	var msgs []InboxMessage
	err = sonic.UnmarshalString(content, &msgs)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)

	var payload taskAssignmentPayload
	err = sonic.UnmarshalString(msgs[0].Text, &payload)
	assert.NoError(t, err)
	assert.Equal(t, string(messageTypeTaskAssignment), payload.Type)
	assert.Equal(t, "42", payload.TaskID)
	assert.Equal(t, "fix bug", payload.Subject)
	assert.Equal(t, "fix the login bug", payload.Description)
	assert.Equal(t, "team-lead", payload.AssignedBy)
	assert.NotEmpty(t, payload.Timestamp)

	expectedPath := filepath.Join("/tmp/test", "teams", teamName, "inboxes", "dev1.json")
	assert.Equal(t, expectedPath, inboxPath)
}
