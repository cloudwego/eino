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

// team_config.go implements teamConfigStore, which manages the persistent
// team config.json (member list, team metadata) with read-write locking.

package team

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

const configFileName = "config.json"

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
}

// makeAgentID returns the agent ID in the format "name@team".
func makeAgentID(name, teamName string) string {
	return name + "@" + teamName
}

// teamConfigStore handles config.json read/write and member management.
type teamConfigStore struct {
	backend Backend
	baseDir string
	lock    *sync.RWMutex
}

func newTeamConfigStore(backend Backend, baseDir string, lock *sync.RWMutex) *teamConfigStore {
	return &teamConfigStore{backend: backend, baseDir: baseDir, lock: lock}
}

// resolveTeamName returns a unique team name. If the given name is already
// taken (e.g. leftover from a previous run), it appends a Unix-nano timestamp
// to avoid collisions — mirroring Claude Code's conflict-resolution behaviour.
func (cm *teamConfigStore) resolveTeamName(ctx context.Context, teamName string) (string, error) {
	path := cm.configFilePath(teamName)
	exists, err := cm.backend.Exists(ctx, path)
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
func (cm *teamConfigStore) CreateTeam(ctx context.Context, teamName, description, leaderName, leaderType string) (*teamConfig, error) {
	cm.lock.Lock()
	defer cm.lock.Unlock()

	resolved, err := cm.resolveTeamName(ctx, teamName)
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
			{
				Name:      leaderName,
				AgentID:   makeAgentID(leaderName, teamName),
				JoinedAt:  time.Now(),
				AgentType: leaderType,
			},
		},
		CreatedAt: time.Now(),
	}

	data, err := sonic.MarshalString(config)
	if err != nil {
		return nil, fmt.Errorf("marshal team config: %w", err)
	}

	// create inboxes dir
	if err := ensureDir(ctx, cm.backend, inboxDirPath(cm.baseDir, teamName)); err != nil {
		return nil, fmt.Errorf("create inboxes dir: %w", err)
	}

	// create tasks dir
	if err := ensureDir(ctx, cm.backend, tasksDirPath(cm.baseDir, teamName)); err != nil {
		return nil, fmt.Errorf("create tasks dir: %w", err)
	}

	// write config.json
	if err := cm.backend.Write(ctx, &WriteRequest{
		FilePath: cm.configFilePath(teamName),
		Content:  data,
	}); err != nil {
		return nil, fmt.Errorf("write config.json: %w", err)
	}

	return config, nil
}

// readConfig reads the team configuration without locking.
// Caller must hold at least cm.lock.RLock().
func (cm *teamConfigStore) readConfig(ctx context.Context, teamName string) (*teamConfig, error) {
	content, err := cm.backend.Read(ctx, &ReadRequest{FilePath: cm.configFilePath(teamName)})
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
// Caller must hold cm.lock.Lock().
func (cm *teamConfigStore) writeConfig(ctx context.Context, teamName string, config *teamConfig) error {
	data, err := sonic.MarshalString(config)
	if err != nil {
		return err
	}
	return cm.backend.Write(ctx, &WriteRequest{
		FilePath: cm.configFilePath(teamName),
		Content:  data,
	})
}

// updateConfig performs an atomic read-modify-write on the team config under a write lock.
func (cm *teamConfigStore) updateConfig(ctx context.Context, teamName string, fn func(cfg *teamConfig) error) error {
	cm.lock.Lock()
	defer cm.lock.Unlock()
	config, err := cm.readConfig(ctx, teamName)
	if err != nil {
		return err
	}
	if err := fn(config); err != nil {
		return err
	}
	return cm.writeConfig(ctx, teamName, config)
}

// readConfigLocked reads config under a read lock.
func (cm *teamConfigStore) readConfigLocked(ctx context.Context, teamName string) (*teamConfig, error) {
	cm.lock.RLock()
	defer cm.lock.RUnlock()
	return cm.readConfig(ctx, teamName)
}

// readConfigWithReadLock reads config under a read lock and passes it to fn for processing.
func (cm *teamConfigStore) readConfigWithReadLock(ctx context.Context, teamName string, fn func(cfg *teamConfig) error) error {
	cm.lock.RLock()
	defer cm.lock.RUnlock()
	config, err := cm.readConfig(ctx, teamName)
	if err != nil {
		return err
	}
	return fn(config)
}

// AddMember adds a new member to the team configuration.
func (cm *teamConfigStore) AddMember(ctx context.Context, teamName string, member teamMember) error {
	return cm.updateConfig(ctx, teamName, func(cfg *teamConfig) error {
		cfg.Members = append(cfg.Members, member)
		return nil
	})
}

// AddMemberWithDeduplicatedName adds a member under a single write lock and
// returns the final member with a unique name assigned.
func (cm *teamConfigStore) AddMemberWithDeduplicatedName(ctx context.Context, teamName string, member teamMember) (teamMember, error) {
	var result teamMember
	err := cm.updateConfig(ctx, teamName, func(cfg *teamConfig) error {
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
			finalName = fmt.Sprintf("%s-%d", baseName, i)
		}
		if _, ok := existing[finalName]; ok {
			return fmt.Errorf("name deduplication exceeded limit (%d) for base name %q", maxDedup, baseName)
		}

		member.Name = finalName
		member.AgentID = makeAgentID(finalName, teamName)
		cfg.Members = append(cfg.Members, member)
		result = member
		return nil
	})
	return result, err
}

// RemoveMember removes a member from the team configuration.
func (cm *teamConfigStore) RemoveMember(ctx context.Context, teamName, memberName string) error {
	return cm.updateConfig(ctx, teamName, func(cfg *teamConfig) error {
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

// HasActiveTeammates checks if there are active teammates (excluding leader).
func (cm *teamConfigStore) HasActiveTeammates(ctx context.Context, teamName string) (bool, error) {
	cfg, err := cm.readConfigLocked(ctx, teamName)
	if err != nil {
		return false, err
	}
	for _, m := range cfg.Members {
		if m.Name != LeaderAgentName {
			return true, nil
		}
	}
	return false, nil
}

// GetActiveTeammateNames returns the names of active teammates (excluding leader).
func (cm *teamConfigStore) GetActiveTeammateNames(ctx context.Context, teamName string) ([]string, error) {
	var names []string
	err := cm.readConfigWithReadLock(ctx, teamName, func(cfg *teamConfig) error {
		for _, m := range cfg.Members {
			if m.Name != LeaderAgentName {
				names = append(names, m.Name)
			}
		}
		return nil
	})
	return names, err
}

// HasMember checks whether the given member exists in the team configuration.
func (cm *teamConfigStore) HasMember(ctx context.Context, teamName, memberName string) (bool, error) {
	var found bool
	err := cm.readConfigWithReadLock(ctx, teamName, func(cfg *teamConfig) error {
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
func (cm *teamConfigStore) DeleteTeam(ctx context.Context, teamName string) error {
	teamDir := teamDirPath(cm.baseDir, teamName)
	taskDir := tasksDirPath(cm.baseDir, teamName)

	if err := deleteDirIfExists(ctx, cm.backend, teamDir); err != nil {
		return fmt.Errorf("delete team dir: %w", err)
	}
	if err := deleteDirIfExists(ctx, cm.backend, taskDir); err != nil {
		return fmt.Errorf("delete task dir: %w", err)
	}

	return nil
}

// configFilePath returns the config.json path for the given team.
// Path: {baseDir}/teams/{teamName}/config.json
func (cm *teamConfigStore) configFilePath(teamName string) string {
	return filepath.Join(teamDirPath(cm.baseDir, teamName), configFileName)
}

// LeadAgentID returns the agent ID of the team leader.
func (cm *teamConfigStore) LeadAgentID(teamName string) string {
	return makeAgentID(LeaderAgentName, teamName)
}
