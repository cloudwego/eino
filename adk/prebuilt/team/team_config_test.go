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
	assert.Equal(t, expected, newConfigStore(conf).configFilePath("myteam"))
}

func TestLeadAgentID(t *testing.T) {
	conf, _ := newTestConfig()
	assert.Equal(t, "team-lead@myteam", newConfigStore(conf).LeadAgentID("myteam"))
	assert.Equal(t, "team-lead@alpha", newConfigStore(conf).LeadAgentID("alpha"))
}

func TestResolveTeamName_NotTaken(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	name, err := newConfigStore(conf).resolveTeamName(ctx, "fresh-team")
	assert.NoError(t, err)
	assert.Equal(t, "fresh-team", name)
}

func TestResolveTeamName_Taken(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	backend.files[newConfigStore(conf).configFilePath("myteam")] = `{}`

	name, err := newConfigStore(conf).resolveTeamName(ctx, "myteam")
	assert.NoError(t, err)
	assert.NotEqual(t, "myteam", name)
	assert.True(t, strings.HasPrefix(name, "myteam-"))
}

// TestResolveTeamName_TakenNearLimitStaysValid guards the length fix: when a
// near-maxNameLength team name collides, appending the timestamp suffix must not
// push the resolved name past maxNameLength or otherwise fail validateTeamName.
func TestResolveTeamName_TakenNearLimitStaysValid(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	base := strings.Repeat("a", maxNameLength)
	backend.files[newConfigStore(conf).configFilePath(base)] = `{}`

	name, err := newConfigStore(conf).resolveTeamName(ctx, base)
	assert.NoError(t, err)
	assert.NotEqual(t, base, name)
	assert.LessOrEqual(t, len(name), maxNameLength)
	assert.NoError(t, validateTeamName(name))
}

func TestCreateTeam(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	cfg, err := newConfigStore(conf).CreateTeam(ctx, "alpha", "test team", "leader1", "specialist")
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

	configPath := newConfigStore(conf).configFilePath("alpha")
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

	cfg, err := newConfigStore(conf).CreateTeam(ctx, "beta", "desc", "boss", "")
	assert.NoError(t, err)
	assert.Equal(t, generalAgentName, cfg.Members[0].AgentType)
}

func TestCreateTeam_NameCollision(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	backend.files[newConfigStore(conf).configFilePath("taken")] = `{}`

	before := time.Now().UnixNano()
	cfg, err := newConfigStore(conf).CreateTeam(ctx, "taken", "desc", "lead", "general")
	assert.NoError(t, err)
	assert.NotEqual(t, "taken", cfg.Name)
	assert.True(t, strings.HasPrefix(cfg.Name, "taken-"))

	suffix := strings.TrimPrefix(cfg.Name, "taken-")
	assert.NotEmpty(t, suffix)

	configPath := newConfigStore(conf).configFilePath(cfg.Name)
	_, ok := backend.files[configPath]
	assert.True(t, ok)
	_ = before
}

func TestReadConfigLocked(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "gamma", "read test", "leader", "type1")
	assert.NoError(t, err)

	cfg, err := newConfigStore(conf).readConfigLocked(ctx, "gamma")
	assert.NoError(t, err)
	assert.Equal(t, "gamma", cfg.Name)
	assert.Equal(t, "read test", cfg.Description)
	assert.Len(t, cfg.Members, 1)
	assert.Equal(t, "leader", cfg.Members[0].Name)
}

func TestUpdateConfig(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "delta", "original", "lead", "type1")
	assert.NoError(t, err)

	err = newConfigStore(conf).updateConfig(ctx, "delta", func(cfg *teamConfig) error {
		cfg.Description = "updated"
		return nil
	})
	assert.NoError(t, err)

	cfg, err := newConfigStore(conf).readConfigLocked(ctx, "delta")
	assert.NoError(t, err)
	assert.Equal(t, "updated", cfg.Description)
}

func TestAddMember(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "epsilon", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "worker1",
		AgentID:   makeAgentID("worker1", "epsilon"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = newConfigStore(conf).AddMember(ctx, "epsilon", member)
	assert.NoError(t, err)

	cfg, err := newConfigStore(conf).readConfigLocked(ctx, "epsilon")
	assert.NoError(t, err)
	assert.Len(t, cfg.Members, 2)
	assert.Equal(t, "worker1", cfg.Members[1].Name)
	assert.Equal(t, "worker1@epsilon", cfg.Members[1].AgentID)
	assert.Equal(t, "coder", cfg.Members[1].AgentType)
}

func TestAddMemberWithDeduplicatedName_Unique(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "zeta", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "unique-agent",
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	result, err := newConfigStore(conf).AddMemberWithDeduplicatedName(ctx, "zeta", member)
	assert.NoError(t, err)
	assert.Equal(t, "unique-agent", result.Name)
	assert.Equal(t, "unique-agent@zeta", result.AgentID)
}

func TestAddMemberWithDeduplicatedName_Duplicate(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "eta", "desc", "lead", "type1")
	assert.NoError(t, err)

	first := teamMember{
		Name:      "agent",
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	_, err = newConfigStore(conf).AddMemberWithDeduplicatedName(ctx, "eta", first)
	assert.NoError(t, err)

	second := teamMember{
		Name:      "agent",
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	result, err := newConfigStore(conf).AddMemberWithDeduplicatedName(ctx, "eta", second)
	assert.NoError(t, err)
	assert.Equal(t, "agent-2", result.Name)
	assert.Equal(t, "agent-2@eta", result.AgentID)
}

func TestAddMemberWithDeduplicatedName_NearLimitStaysValid(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	store := newConfigStore(conf)
	_, err := store.CreateTeam(ctx, "theta", "desc", "lead", "type1")
	assert.NoError(t, err)

	// A base name at the maximum length collides, so dedup must append a suffix
	// without producing a name that exceeds maxNameLength or otherwise fails the
	// member-name rules.
	base := strings.Repeat("a", maxNameLength)
	first := teamMember{Name: base, JoinedAt: time.Now()}
	r1, err := store.AddMemberWithDeduplicatedName(ctx, "theta", first)
	assert.NoError(t, err)
	assert.Equal(t, base, r1.Name)

	second := teamMember{Name: base, JoinedAt: time.Now()}
	r2, err := store.AddMemberWithDeduplicatedName(ctx, "theta", second)
	assert.NoError(t, err)
	assert.LessOrEqual(t, len(r2.Name), maxNameLength)
	assert.NoError(t, validateMemberName(r2.Name))
	assert.Equal(t, makeAgentID(r2.Name, "theta"), r2.AgentID)
}

func TestRemoveMember(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "iota", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "removable",
		AgentID:   makeAgentID("removable", "iota"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = newConfigStore(conf).AddMember(ctx, "iota", member)
	assert.NoError(t, err)

	cfg, err := newConfigStore(conf).readConfigLocked(ctx, "iota")
	assert.NoError(t, err)
	assert.Len(t, cfg.Members, 2)

	err = newConfigStore(conf).RemoveMember(ctx, "iota", "removable")
	assert.NoError(t, err)

	cfg, err = newConfigStore(conf).readConfigLocked(ctx, "iota")
	assert.NoError(t, err)
	assert.Len(t, cfg.Members, 1)
	for _, m := range cfg.Members {
		assert.NotEqual(t, "removable", m.Name)
	}
}

func TestHasMember_Found(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "nu", "desc", "lead", "type1")
	assert.NoError(t, err)

	member := teamMember{
		Name:      "target",
		AgentID:   makeAgentID("target", "nu"),
		AgentType: "coder",
		JoinedAt:  time.Now(),
	}
	err = newConfigStore(conf).AddMember(ctx, "nu", member)
	assert.NoError(t, err)

	found, err := newConfigStore(conf).HasMember(ctx, "nu", "target")
	assert.NoError(t, err)
	assert.True(t, found)
}

func TestHasMember_NotFound(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "xi", "desc", "lead", "type1")
	assert.NoError(t, err)

	found, err := newConfigStore(conf).HasMember(ctx, "xi", "nonexistent")
	assert.NoError(t, err)
	assert.False(t, found)
}

func TestDeleteTeam(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	_, err := newConfigStore(conf).CreateTeam(ctx, "omicron", "desc", "lead", "type1")
	assert.NoError(t, err)

	configPath := newConfigStore(conf).configFilePath("omicron")
	_, ok := backend.files[configPath]
	assert.True(t, ok)

	teamDir := filepath.Join("/tmp/test", "teams", "omicron")
	inboxDir := filepath.Join(teamDir, "inboxes")
	tasksDir := filepath.Join("/tmp/test", "tasks", "omicron")
	assert.True(t, backend.dirs[inboxDir])
	assert.True(t, backend.dirs[tasksDir])

	backend.dirs[teamDir] = true
	backend.dirs[tasksDir] = true

	err = newConfigStore(conf).DeleteTeam(ctx, "omicron")
	assert.NoError(t, err)

	_, ok = backend.files[configPath]
	assert.False(t, ok)

	assert.False(t, backend.dirs[teamDir])
	assert.False(t, backend.dirs[tasksDir])
}

func TestReadConfig_InvalidJSON(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	configPath := newConfigStore(conf).configFilePath("badteam")
	backend.files[configPath] = `not valid json`

	conf.state.cfgLock.RLock()
	_, err := newConfigStore(conf).readConfig(ctx, "badteam")
	conf.state.cfgLock.RUnlock()
	assert.Error(t, err)
}

// TestReadConfig_NilContent ensures readConfig does not panic when the backend
// reports a missing file as (nil, nil) — a result the Backend.Read contract
// permits — and instead surfaces a descriptive error.
func TestReadConfig_NilContent(t *testing.T) {
	backend := &nilContentBackend{inMemoryBackend: newInMemoryBackend()}
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()
	ctx := context.Background()

	conf.state.cfgLock.RLock()
	cfg, err := newConfigStore(conf).readConfig(ctx, "ghostteam")
	conf.state.cfgLock.RUnlock()

	assert.Nil(t, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing or empty")
}

// TestReadConfig_EmptyContent ensures an empty (but successfully read) config
// file is treated as an error rather than unmarshalled into a zero-value team.
func TestReadConfig_EmptyContent(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	configPath := newConfigStore(conf).configFilePath("emptyteam")
	backend.files[configPath] = ""

	conf.state.cfgLock.RLock()
	cfg, err := newConfigStore(conf).readConfig(ctx, "emptyteam")
	conf.state.cfgLock.RUnlock()

	assert.Nil(t, cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing or empty")
}

func TestWriteConfig_BackendWriteError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("write failed"))

	cfg := &teamConfig{Name: "test", Members: []teamMember{}}
	conf.state.cfgLock.Lock()
	err := newConfigStore(conf).writeConfig(context.Background(), "test", cfg)
	conf.state.cfgLock.Unlock()
	assert.Error(t, err)
}

func TestUpdateConfig_ReadConfigError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("read failed"))

	err := newConfigStore(conf).updateConfig(context.Background(), "nonexistent", func(cfg *teamConfig) error {
		return nil
	})
	assert.Error(t, err)
}

func TestCreateTeam_EnsureDirError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("dir error"))

	_, err := newConfigStore(conf).CreateTeam(context.Background(), "newteam", "desc", "lead", "type1")
	assert.Error(t, err)
}

func TestDeleteTeam_BackendError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("delete failed"))

	err := newConfigStore(conf).DeleteTeam(context.Background(), "someteam")
	assert.Error(t, err)
}

func TestResolveTeamName_BackendReadError(t *testing.T) {
	conf := newTestConfigWithErrBackend(errors.New("exists error"))

	_, err := newConfigStore(conf).resolveTeamName(context.Background(), "someteam")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exists error")
}
