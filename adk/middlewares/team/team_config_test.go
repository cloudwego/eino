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
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func newTestConfig() (*Config, *inMemoryBackend) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()
	return conf, backend
}

func newTestConfigWithErrBackend(err error) *Config {
	eb := newErrBackend(err)
	conf := &Config{Backend: eb, BaseDir: "/tmp/test"}
	conf.ensureInit()
	return conf
}

func TestMakeAgentID(t *testing.T) {
	assert.Equal(t, "alice@myteam", makeAgentID("alice", "myteam"))
	assert.Equal(t, "bob@dev", makeAgentID("bob", "dev"))
	assert.Equal(t, "@empty", makeAgentID("", "empty"))
}

func TestConfigFilePath(t *testing.T) {
	conf, _ := newTestConfig()
	expected := filepath.Join("/tmp/test", "teams", "myteam", "config.json")
	assert.Equal(t, expected, conf.configFilePath("myteam"))
}

func TestLeadAgentID(t *testing.T) {
	conf, _ := newTestConfig()
	assert.Equal(t, "team-lead@myteam", conf.LeadAgentID("myteam"))
	assert.Equal(t, "team-lead@alpha", conf.LeadAgentID("alpha"))
}

func TestResolveTeamName_NotTaken(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	name, err := conf.resolveTeamName(ctx, "fresh-team")
	assert.NoError(t, err)
	assert.Equal(t, "fresh-team", name)
}

func TestResolveTeamName_Taken(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	backend.files[conf.configFilePath("myteam")] = `{}`

	name, err := conf.resolveTeamName(ctx, "myteam")
	assert.NoError(t, err)
	assert.NotEqual(t, "myteam", name)
	assert.True(t, strings.HasPrefix(name, "myteam-"))
}

func TestCreateTeam(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	cfg, err := conf.CreateTeam(ctx, "alpha", "test team", "leader1", "specialist")
	assert.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, "alpha", cfg.Name)
	assert.Equal(t, "test team", cfg.Description)
	assert.Equal(t, "leader1@alpha", cfg.LeadAgentID)
	assert.Len(t, cfg.Members, 1)
	assert.Equal(t, "leader1", cfg.Members[0].Name)
	assert.Equal(t, "leader1@alpha", cfg.Members[0].AgentID)
	assert.Equal(t, "specialist", cfg.Members[0].AgentType)
	assert.False(t, cfg.CreatedAt.IsZero())
	assert.False(t, cfg.Members[0].JoinedAt.IsZero())

	configPath := conf.configFilePath("alpha")
	_, ok := backend.files[configPath]
	assert.True(t, ok)

	inboxDir := filepath.Join("/tmp/test", "teams", "alpha", "inboxes")
	assert.True(t, backend.dirs[inboxDir])

	tasksDir := filepath.Join("/tmp/test", "tasks", "alpha")
	assert.True(t, backend.dirs[tasksDir])
}

func TestCreateTeam_EmptyLeaderType(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	cfg, err := conf.CreateTeam(ctx, "beta", "desc", "boss", "")
	assert.NoError(t, err)
	assert.Equal(t, generalAgentName, cfg.Members[0].AgentType)
}

func TestCreateTeam_NameCollision(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	backend.files[conf.configFilePath("taken")] = `{}`

	before := time.Now().UnixNano()
	cfg, err := conf.CreateTeam(ctx, "taken", "desc", "lead", "general")
	assert.NoError(t, err)
	assert.NotEqual(t, "taken", cfg.Name)
	assert.True(t, strings.HasPrefix(cfg.Name, "taken-"))

	suffix := strings.TrimPrefix(cfg.Name, "taken-")
	assert.NotEmpty(t, suffix)

	configPath := conf.configFilePath(cfg.Name)
	_, ok := backend.files[configPath]
	assert.True(t, ok)
	_ = before
}

func TestReadConfigLocked(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "gamma", "read test", "leader", "type1")
	assert.NoError(t, err)

	cfg, err := conf.readConfigLocked(ctx, "gamma")
	assert.NoError(t, err)
	assert.Equal(t, "gamma", cfg.Name)
	assert.Equal(t, "read test", cfg.Description)
	assert.Len(t, cfg.Members, 1)
	assert.Equal(t, "leader", cfg.Members[0].Name)
}

func TestUpdateConfig(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "delta", "original", "lead", "type1")
	assert.NoError(t, err)

	err = conf.updateConfig(ctx, "delta", func(cfg *teamConfig) error {
		cfg.Description = "updated"
		return nil
	})
	assert.NoError(t, err)

	cfg, err := conf.readConfigLocked(ctx, "delta")
	assert.NoError(t, err)
	assert.Equal(t, "updated", cfg.Description)
}

func TestAddMember(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "epsilon", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "worker1",
		AgentID:   makeAgentID("worker1", "epsilon"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = conf.AddMember(ctx, "epsilon", member)
	assert.NoError(t, err)

	cfg, err := conf.readConfigLocked(ctx, "epsilon")
	assert.NoError(t, err)
	assert.Len(t, cfg.Members, 2)
	assert.Equal(t, "worker1", cfg.Members[1].Name)
	assert.Equal(t, "worker1@epsilon", cfg.Members[1].AgentID)
	assert.Equal(t, "coder", cfg.Members[1].AgentType)
}

func TestAddMemberWithDeduplicatedName_Unique(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "zeta", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "unique-agent",
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	result, err := conf.AddMemberWithDeduplicatedName(ctx, "zeta", member)
	assert.NoError(t, err)
	assert.Equal(t, "unique-agent", result.Name)
	assert.Equal(t, "unique-agent@zeta", result.AgentID)
}

func TestAddMemberWithDeduplicatedName_Duplicate(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "eta", "desc", "lead", "type1")
	assert.NoError(t, err)

	first := teamMember{
		Name:      "agent",
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	_, err = conf.AddMemberWithDeduplicatedName(ctx, "eta", first)
	assert.NoError(t, err)

	second := teamMember{
		Name:      "agent",
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	result, err := conf.AddMemberWithDeduplicatedName(ctx, "eta", second)
	assert.NoError(t, err)
	assert.Equal(t, "agent-2", result.Name)
	assert.Equal(t, "agent-2@eta", result.AgentID)
}

func TestRemoveMember(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "iota", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "removable",
		AgentID:   makeAgentID("removable", "iota"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = conf.AddMember(ctx, "iota", member)
	assert.NoError(t, err)

	cfg, err := conf.readConfigLocked(ctx, "iota")
	assert.NoError(t, err)
	assert.Len(t, cfg.Members, 2)

	err = conf.RemoveMember(ctx, "iota", "removable")
	assert.NoError(t, err)

	cfg, err = conf.readConfigLocked(ctx, "iota")
	assert.NoError(t, err)
	assert.Len(t, cfg.Members, 1)
	for _, m := range cfg.Members {
		assert.NotEqual(t, "removable", m.Name)
	}
}

func TestHasActiveTeammates_NoTeammates(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "kappa", "desc", LeaderAgentName, "type1")
	assert.NoError(t, err)

	has, err := conf.HasActiveTeammates(ctx, "kappa")
	assert.NoError(t, err)
	assert.False(t, has)
}

func TestHasActiveTeammates_WithTeammate(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "lambda", "desc", LeaderAgentName, "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "worker",
		AgentID:   makeAgentID("worker", "lambda"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = conf.AddMember(ctx, "lambda", member)
	assert.NoError(t, err)

	has, err := conf.HasActiveTeammates(ctx, "lambda")
	assert.NoError(t, err)
	assert.True(t, has)
}

func TestGetActiveTeammateNames(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "mu", "desc", LeaderAgentName, "type1")
	assert.NoError(t, err)

	member1 := teamMember{
		Name:      "dev1",
		AgentID:   makeAgentID("dev1", "mu"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	member2 := teamMember{
		Name:      "dev2",
		AgentID:   makeAgentID("dev2", "mu"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = conf.AddMember(ctx, "mu", member1)
	assert.NoError(t, err)
	err = conf.AddMember(ctx, "mu", member2)
	assert.NoError(t, err)

	names, err := conf.GetActiveTeammateNames(ctx, "mu")
	assert.NoError(t, err)
	assert.Len(t, names, 2)
	assert.Contains(t, names, "dev1")
	assert.Contains(t, names, "dev2")
	assert.NotContains(t, names, LeaderAgentName)
}

func TestGetActiveTeammateNames_ExcludesIdleTeammates(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "mu-idle", "desc", LeaderAgentName, "type1")
	assert.NoError(t, err)

	err = conf.AddMember(ctx, "mu-idle", teamMember{
		Name:      "dev1",
		AgentID:   makeAgentID("dev1", "mu-idle"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	})
	assert.NoError(t, err)
	err = conf.AddMember(ctx, "mu-idle", teamMember{
		Name:      "dev2",
		AgentID:   makeAgentID("dev2", "mu-idle"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	})
	assert.NoError(t, err)
	err = conf.SetMemberActive(ctx, "mu-idle", "dev2", false)
	assert.NoError(t, err)

	names, err := conf.GetActiveTeammateNames(ctx, "mu-idle")
	assert.NoError(t, err)
	assert.Equal(t, []string{"dev1"}, names)
}

func TestHasMember_Found(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "nu", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "target",
		AgentID:   makeAgentID("target", "nu"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = conf.AddMember(ctx, "nu", member)
	assert.NoError(t, err)

	found, err := conf.HasMember(ctx, "nu", "target")
	assert.NoError(t, err)
	assert.True(t, found)
}

func TestHasMember_NotFound(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "xi", "desc", "lead", "type1")
	assert.NoError(t, err)

	found, err := conf.HasMember(ctx, "xi", "nonexistent")
	assert.NoError(t, err)
	assert.False(t, found)
}

func TestDeleteTeam(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "omicron", "desc", "lead", "type1")
	assert.NoError(t, err)

	configPath := conf.configFilePath("omicron")
	_, ok := backend.files[configPath]
	assert.True(t, ok)

	teamDir := filepath.Join("/tmp/test", "teams", "omicron")
	inboxDir := filepath.Join(teamDir, "inboxes")
	tasksDir := filepath.Join("/tmp/test", "tasks", "omicron")
	assert.True(t, backend.dirs[inboxDir])
	assert.True(t, backend.dirs[tasksDir])

	backend.dirs[teamDir] = true
	backend.dirs[tasksDir] = true

	err = conf.DeleteTeam(ctx, "omicron")
	assert.NoError(t, err)

	_, ok = backend.files[configPath]
	assert.False(t, ok)

	assert.False(t, backend.dirs[teamDir])
	assert.False(t, backend.dirs[tasksDir])
}

func TestReadConfig_InvalidJSON(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	configPath := conf.configFilePath("badteam")
	backend.files[configPath] = `not valid json`

	conf.state.cfgLock.RLock()
	_, err := conf.readConfig(ctx, "badteam")
	conf.state.cfgLock.RUnlock()
	assert.Error(t, err)
}

func TestWriteConfig_BackendWriteError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("write failed"))

	cfg := &teamConfig{Name: "test", Members: []teamMember{}}
	conf.state.cfgLock.Lock()
	err := conf.writeConfig(context.Background(), "test", cfg)
	conf.state.cfgLock.Unlock()
	assert.Error(t, err)
}

func TestUpdateConfig_ReadConfigError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("read failed"))

	err := conf.updateConfig(context.Background(), "nonexistent", func(cfg *teamConfig) error {
		return nil
	})
	assert.Error(t, err)
}

func TestCreateTeam_EnsureDirError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("dir error"))

	_, err := conf.CreateTeam(context.Background(), "newteam", "desc", "lead", "type1")
	assert.Error(t, err)
}

func TestDeleteTeam_BackendError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("delete failed"))

	err := conf.DeleteTeam(context.Background(), "someteam")
	assert.Error(t, err)
}

func TestHasActiveTeammates_ReadConfigError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("read failed"))

	_, err := conf.HasActiveTeammates(context.Background(), "someteam")
	assert.Error(t, err)
}

func TestResolveTeamName_BackendReadError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("exists error"))

	_, err := conf.resolveTeamName(context.Background(), "someteam")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exists error")
}
