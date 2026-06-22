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

// team_config.go manages the persistent team config.json (member list, team
// metadata) with read-write locking.
//
// All runtime operations live on the unexported configStore rather than on the
// public Config: Config is purely declarative configuration (Backend, BaseDir,
// Interval) supplied by the caller, while configStore holds the team-management
// behavior used internally by the tool and lifecycle layers. This keeps the
// public API surface small and prevents callers from reaching into team
// bookkeeping that is meant to be driven by the Runner.

package team

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
)

const configFileName = "config.json"

// configStore is the internal façade over a Config that performs all team
// config.json read-modify-write operations. It is created once per Config via
// newConfigStore and shared by the lifecycle and tool layers.
type configStore struct {
	conf *Config
}

// newConfigStore wraps a Config so the team runtime can perform config.json
// operations without exposing them on the public Config type.
func newConfigStore(conf *Config) *configStore {
	return &configStore{conf: conf}
}

// teamConfig represents the team configuration stored in config.json.
type teamConfig struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	LeadAgentID string       `json:"leadAgentId,omitempty"`
	Members     []teamMember `json:"members"`
	CreatedAt   time.Time    `json:"createdAt"`
}

// teamMember represents a member in the team configuration.
type teamMember struct {
	Name      string    `json:"name"`
	AgentID   string    `json:"agentId,omitempty"`
	AgentType string    `json:"agentType,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	JoinedAt  time.Time `json:"joinedAt"`
	IsActive  *bool     `json:"isActive,omitempty"`
}

// makeAgentID returns the agent ID in the format "name@team".
func makeAgentID(name, teamName string) string {
	return name + "@" + teamName
}

func boolPtr(v bool) *bool {
	return &v
}

func withDefaultMemberActivity(member teamMember) teamMember {
	if member.IsActive == nil {
		member.IsActive = boolPtr(true)
	}
	return member
}

// resolveTeamName returns a unique team name. If the given name is already
// taken (e.g. leftover from a previous run), it appends a Unix-nano timestamp
// to avoid collisions
func (s *configStore) resolveTeamName(ctx context.Context, teamName string) (string, error) {
	path := s.configFilePath(teamName)
	exists, err := s.conf.Backend.Exists(ctx, path)
	if err != nil {
		return "", fmt.Errorf("check team %q exists error: %w", teamName, err)
	}
	if !exists {
		return teamName, nil
	}
	// Name taken — generate a timestamped alternative.
	resolved := fmt.Sprintf("%s-%d", teamName, time.Now().UnixNano())
	return resolved, nil
}

// CreateTeam creates the team directory structure and config.json.
// If teamName is already taken, a timestamped suffix is appended automatically.
func (s *configStore) CreateTeam(ctx context.Context, teamName, description, leaderName, leaderType string) (*teamConfig, error) {
	s.conf.state.cfgLock.Lock()
	defer s.conf.state.cfgLock.Unlock()

	resolved, err := s.resolveTeamName(ctx, teamName)
	if err != nil {
		return nil, err
	}

	teamName = resolved

	if leaderType == "" {
		leaderType = generalAgentName
	}

	config := &teamConfig{
		Name:        teamName,
		Description: description,
		LeadAgentID: makeAgentID(leaderName, teamName),
		Members: []teamMember{
			withDefaultMemberActivity(teamMember{
				Name:      leaderName,
				AgentID:   makeAgentID(leaderName, teamName),
				JoinedAt:  time.Now(),
				AgentType: leaderType,
			}),
		},
		CreatedAt: time.Now(),
	}

	data, err := sonic.MarshalString(config)
	if err != nil {
		return nil, fmt.Errorf("marshal team config: %w", err)
	}

	// create inboxes dir
	if err := ensureDir(ctx, s.conf.Backend, inboxDirPath(s.conf.BaseDir, teamName)); err != nil {
		return nil, fmt.Errorf("create inboxes dir: %w", err)
	}

	// create tasks dir
	if err := ensureDir(ctx, s.conf.Backend, tasksDirPath(s.conf.BaseDir, teamName)); err != nil {
		return nil, fmt.Errorf("create tasks dir: %w", err)
	}

	// write config.json
	if err := s.conf.Backend.Write(ctx, &WriteRequest{
		FilePath: s.configFilePath(teamName),
		Content:  data,
	}); err != nil {
		return nil, fmt.Errorf("write config.json: %w", err)
	}

	return config, nil
}

// readConfig reads the team configuration without locking.
// Caller must hold at least s.conf.state.cfgLock.RLock().
func (s *configStore) readConfig(ctx context.Context, teamName string) (*teamConfig, error) {
	content, err := s.conf.Backend.Read(ctx, &ReadRequest{FilePath: s.configFilePath(teamName)})
	if err != nil {
		return nil, err
	}
	var config teamConfig
	if err := sonic.UnmarshalString(content.Content, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// writeConfig writes the team configuration without locking.
// Caller must hold s.conf.state.cfgLock.Lock().
func (s *configStore) writeConfig(ctx context.Context, teamName string, config *teamConfig) error {
	data, err := sonic.MarshalString(config)
	if err != nil {
		return err
	}
	return s.conf.Backend.Write(ctx, &WriteRequest{
		FilePath: s.configFilePath(teamName),
		Content:  data,
	})
}

// updateConfig performs an atomic read-modify-write on the team config under a write lock.
func (s *configStore) updateConfig(ctx context.Context, teamName string, fn func(cfg *teamConfig) error) error {
	s.conf.state.cfgLock.Lock()
	defer s.conf.state.cfgLock.Unlock()
	config, err := s.readConfig(ctx, teamName)
	if err != nil {
		return err
	}
	if err := fn(config); err != nil {
		return err
	}
	return s.writeConfig(ctx, teamName, config)
}

// readConfigLocked reads config under a read lock.
func (s *configStore) readConfigLocked(ctx context.Context, teamName string) (*teamConfig, error) {
	s.conf.state.cfgLock.RLock()
	defer s.conf.state.cfgLock.RUnlock()
	return s.readConfig(ctx, teamName)
}

// readConfigWithReadLock reads config under a read lock and passes it to fn for processing.
func (s *configStore) readConfigWithReadLock(ctx context.Context, teamName string, fn func(cfg *teamConfig) error) error {
	s.conf.state.cfgLock.RLock()
	defer s.conf.state.cfgLock.RUnlock()
	config, err := s.readConfig(ctx, teamName)
	if err != nil {
		return err
	}
	return fn(config)
}

// AddMember adds a new member to the team configuration.
func (s *configStore) AddMember(ctx context.Context, teamName string, member teamMember) error {
	return s.updateConfig(ctx, teamName, func(cfg *teamConfig) error {
		cfg.Members = append(cfg.Members, withDefaultMemberActivity(member))
		return nil
	})
}

// AddMemberWithDeduplicatedName adds a member under a single write lock and
// returns the final member with a unique name assigned.
func (s *configStore) AddMemberWithDeduplicatedName(ctx context.Context, teamName string, member teamMember) (teamMember, error) {
	var result teamMember
	err := s.updateConfig(ctx, teamName, func(cfg *teamConfig) error {
		existing := make(map[string]struct{}, len(cfg.Members))
		for _, m := range cfg.Members {
			existing[m.Name] = struct{}{}
		}

		baseName := member.Name
		finalName := baseName
		const maxDedup = 1000
		for i := 2; i <= maxDedup; i++ {
			if _, ok := existing[finalName]; !ok {
				break
			}
			finalName = suffixedMemberName(baseName, i)
		}
		if _, ok := existing[finalName]; ok {
			return fmt.Errorf("name deduplication exceeded limit (%d) for base name %q", maxDedup, baseName)
		}

		// The base name was validated upstream, but appending a "-N" suffix can
		// push the result past maxNameLength (and thus past the filesystem path
		// limit suffixedMemberName guards against). Re-validate the final name so
		// the same constraints enforced on caller-supplied names also hold for the
		// auto-generated one before it becomes an AgentID and inbox path component.
		if err := validateMemberName(finalName); err != nil {
			return fmt.Errorf("deduplicated %w", err)
		}

		member.Name = finalName
		member.AgentID = makeAgentID(finalName, teamName)
		member = withDefaultMemberActivity(member)
		cfg.Members = append(cfg.Members, member)
		result = member
		return nil
	})
	return result, err
}

func (s *configStore) SetMemberActive(ctx context.Context, teamName, memberName string, active bool) error {
	return s.updateConfig(ctx, teamName, func(cfg *teamConfig) error {
		for i := range cfg.Members {
			if cfg.Members[i].Name != memberName {
				continue
			}
			cfg.Members[i].IsActive = boolPtr(active)
			return nil
		}
		return nil
	})
}

// RemoveMember removes a member from the team configuration.
func (s *configStore) RemoveMember(ctx context.Context, teamName, memberName string) error {
	return s.updateConfig(ctx, teamName, func(cfg *teamConfig) error {
		members := make([]teamMember, 0, len(cfg.Members))
		for _, m := range cfg.Members {
			if m.Name != memberName {
				members = append(members, m)
			}
		}
		cfg.Members = members
		return nil
	})
}

// HasMember checks whether the given member exists in the team configuration.
func (s *configStore) HasMember(ctx context.Context, teamName, memberName string) (bool, error) {
	var found bool
	err := s.readConfigWithReadLock(ctx, teamName, func(cfg *teamConfig) error {
		for _, m := range cfg.Members {
			if m.Name == memberName {
				found = true
				return nil
			}
		}
		return nil
	})
	return found, err
}

// DeleteTeam removes the team directory and tasks directory.
func (s *configStore) DeleteTeam(ctx context.Context, teamName string) error {
	s.conf.state.cfgLock.Lock()
	defer s.conf.state.cfgLock.Unlock()

	teamDir := teamDirPath(s.conf.BaseDir, teamName)
	taskDir := tasksDirPath(s.conf.BaseDir, teamName)

	if err := deleteDirIfExists(ctx, s.conf.Backend, teamDir); err != nil {
		return fmt.Errorf("delete team dir: %w", err)
	}
	if err := deleteDirIfExists(ctx, s.conf.Backend, taskDir); err != nil {
		return fmt.Errorf("delete task dir: %w", err)
	}

	return nil
}

// configFilePath returns the config.json path for the given team.
// Path: {baseDir}/teams/{teamName}/config.json
func (s *configStore) configFilePath(teamName string) string {
	return filepath.Join(teamDirPath(s.conf.BaseDir, teamName), configFileName)
}

// LeadAgentID returns the agent ID of the team leader.
func (s *configStore) LeadAgentID(teamName string) string {
	return makeAgentID(LeaderAgentName, teamName)
}
