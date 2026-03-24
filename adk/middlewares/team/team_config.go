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
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/middlewares/plantask"
)

const generalAgentName = "general-purpose"

// teamConfigStore handles config.json read/write and member management.
type teamConfigStore struct {
	backend  Backend
	baseDir  string
	lock     *sync.RWMutex
	taskLock *sync.Mutex
}

func newTeamConfigStore(backend Backend, baseDir string, lock *sync.RWMutex, taskLock *sync.Mutex) *teamConfigStore {
	return &teamConfigStore{backend: backend, baseDir: baseDir, lock: lock, taskLock: taskLock}
}

func newTeamConfigStoreFromConfig(conf *Config) *teamConfigStore {
	return newTeamConfigStore(conf.Backend, conf.BaseDir, conf.InboxLocks.ForInbox("__config__"), conf.TaskLock)
}

func (cm *teamConfigStore) withReadLock(fn func() error) error {
	cm.lock.RLock()
	defer cm.lock.RUnlock()
	return fn()
}

func (cm *teamConfigStore) withWriteLock(fn func() error) error {
	cm.lock.Lock()
	defer cm.lock.Unlock()
	return fn()
}

// resolveTeamName returns a unique team name. If the given name is already
// taken (e.g. leftover from a previous run), it appends a Unix-nano timestamp
// to avoid collisions — mirroring Claude Code's conflict-resolution behaviour.
func (cm *teamConfigStore) resolveTeamName(ctx context.Context, teamName string) (string, error) {
	resolved := teamName
	for {
		path := cm.configFilePath(resolved)
		exists, err := fileExists(ctx, cm.backend, path)
		if err != nil {
			return "", fmt.Errorf("check team %q exists error: %w", resolved, err)
		}
		if !exists {
			return resolved, nil
		}
		// Name taken — generate a timestamped alternative.
		suffix := fmt.Sprintf("-%d", time.Now().UnixNano())
		resolved = makeResolvedTeamName(teamName, suffix)
		if err := validateTeamName(resolved); err != nil {
			return "", fmt.Errorf("resolved team name %q invalid: %w", resolved, err)
		}
	}
}

func makeResolvedTeamName(base, suffix string) string {
	trimmed := strings.TrimSpace(base)
	if trimmed == "" {
		trimmed = "team"
	}
	maxBaseLen := maxTeamNameLength - len(suffix)
	if maxBaseLen < 1 {
		maxBaseLen = 1
	}
	if len(trimmed) > maxBaseLen {
		trimmed = trimmed[:maxBaseLen]
	}
	return trimmed + suffix
}

// CreateTeam creates the team directory structure and config.json.
// If teamName is already taken, a timestamped suffix is appended automatically.
func (cm *teamConfigStore) CreateTeam(ctx context.Context, teamName, description, leaderName, leaderType string) (*teamConfig, error) {
	cm.lock.Lock()
	defer cm.lock.Unlock()

	if err := validateTeamName(teamName); err != nil {
		return nil, err
	}
	if err := validateAgentName(leaderName); err != nil {
		return nil, err
	}

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

	// ensure base team/tasks directories exist (backend may not support recursive mkdir)
	if err := ensureDir(ctx, cm.backend, filepath.Join(cm.baseDir, "teams")); err != nil {
		return nil, fmt.Errorf("create teams root dir: %w", err)
	}
	if err := ensureDir(ctx, cm.backend, filepath.Join(cm.baseDir, "tasks")); err != nil {
		return nil, fmt.Errorf("create tasks root dir: %w", err)
	}
	if err := ensureDir(ctx, cm.backend, teamDirPath(cm.baseDir, teamName)); err != nil {
		return nil, fmt.Errorf("create team dir: %w", err)
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
	if err := sonic.UnmarshalString(content, &config); err != nil {
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

// ReadConfigWithLock reads the team configuration with read lock.
func (cm *teamConfigStore) ReadConfigWithLock(ctx context.Context, teamName string) (*teamConfig, error) {
	var config *teamConfig
	err := cm.withReadLock(func() error {
		var readErr error
		config, readErr = cm.readConfig(ctx, teamName)
		return readErr
	})
	return config, err
}

// WriteConfigWithLock writes the team configuration with write lock.
func (cm *teamConfigStore) WriteConfigWithLock(ctx context.Context, teamName string, config *teamConfig) error {
	return cm.withWriteLock(func() error {
		return cm.writeConfig(ctx, teamName, config)
	})
}

// AddMember adds a new member to the team configuration.
// The read-modify-write is done under a single write lock to avoid TOCTOU races.
func (cm *teamConfigStore) AddMember(ctx context.Context, teamName string, member teamMember) error {
	cm.lock.Lock()
	defer cm.lock.Unlock()
	if err := validateTeamName(teamName); err != nil {
		return err
	}
	if err := validateAgentName(member.Name); err != nil {
		return err
	}
	config, err := cm.readConfig(ctx, teamName)
	if err != nil {
		return err
	}
	config.Members = append(config.Members, member)
	return cm.writeConfig(ctx, teamName, config)
}

// AddMemberWithDeduplicatedName adds a member under a single write lock and
// returns the final member with a unique name assigned.
func (cm *teamConfigStore) AddMemberWithDeduplicatedName(ctx context.Context, teamName string, member teamMember) (teamMember, error) {
	cm.lock.Lock()
	defer cm.lock.Unlock()

	if err := validateTeamName(teamName); err != nil {
		return teamMember{}, err
	}
	if err := validateAgentName(member.Name); err != nil {
		return teamMember{}, err
	}

	config, err := cm.readConfig(ctx, teamName)
	if err != nil {
		return teamMember{}, err
	}

	finalName := deduplicateNameWithMembers(config.Members, member.Name)
	member.Name = finalName
	member.AgentID = makeAgentID(finalName, teamName)
	config.Members = append(config.Members, member)

	if err := cm.writeConfig(ctx, teamName, config); err != nil {
		return teamMember{}, err
	}

	return member, nil
}

// RemoveMember removes a member from the team configuration.
// The read-modify-write is done under a single write lock to avoid TOCTOU races.
func (cm *teamConfigStore) RemoveMember(ctx context.Context, teamName, memberName string) error {
	cm.lock.Lock()
	defer cm.lock.Unlock()
	config, err := cm.readConfig(ctx, teamName)
	if err != nil {
		return err
	}
	members := make([]teamMember, 0, len(config.Members))
	for _, m := range config.Members {
		if m.Name != memberName {
			members = append(members, m)
		}
	}
	config.Members = members
	return cm.writeConfig(ctx, teamName, config)
}

// DeduplicateName returns a unique name by appending -2, -3, etc. if needed.
func (cm *teamConfigStore) DeduplicateName(ctx context.Context, teamName, name string) (string, error) {
	cm.lock.RLock()
	defer cm.lock.RUnlock()
	if err := validateTeamName(teamName); err != nil {
		return "", err
	}
	if err := validateAgentName(name); err != nil {
		return "", err
	}
	config, err := cm.readConfig(ctx, teamName)
	if err != nil {
		return "", err
	}

	return deduplicateNameWithMembers(config.Members, name), nil
}

func deduplicateNameWithMembers(members []teamMember, baseName string) string {
	existing := make(map[string]struct{}, len(members))
	for _, m := range members {
		existing[m.Name] = struct{}{}
	}

	if _, ok := existing[baseName]; !ok {
		return baseName
	}

	trimmedBase := baseName
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		maxBaseLen := maxTeamNameLength - len(suffix)
		if maxBaseLen < 1 {
			maxBaseLen = 1
		}
		if len(trimmedBase) > maxBaseLen {
			trimmedBase = trimmedBase[:maxBaseLen]
		}
		finalName := trimmedBase + suffix
		if _, ok := existing[finalName]; !ok {
			return finalName
		}
	}
}

// HasActiveTeammates checks if there are active teammates (excluding leader).
func (cm *teamConfigStore) HasActiveTeammates(ctx context.Context, teamName string) (bool, error) {
	var hasActive bool
	err := cm.withReadLock(func() error {
		config, readErr := cm.readConfig(ctx, teamName)
		if readErr != nil {
			return readErr
		}
		for _, m := range config.Members {
			if m.Name != LeaderAgentName {
				hasActive = true
				return nil
			}
		}
		return nil
	})
	return hasActive, err
}

// HasMember checks whether the given member exists in the team configuration.
func (cm *teamConfigStore) HasMember(ctx context.Context, teamName, memberName string) (bool, error) {
	var exists bool
	err := cm.withReadLock(func() error {
		config, readErr := cm.readConfig(ctx, teamName)
		if readErr != nil {
			return readErr
		}
		for _, m := range config.Members {
			if m.Name == memberName {
				exists = true
				return nil
			}
		}
		return nil
	})
	return exists, err
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
	return filepath.Join(teamDirPath(cm.baseDir, teamName), "config.json")
}

// LeadAgentID returns the agent ID of the team leader.
func (cm *teamConfigStore) LeadAgentID(teamName string) string {
	return makeAgentID(LeaderAgentName, teamName)
}

// taskEntry is a minimal task representation for reading/writing task JSON files.
type taskEntry struct {
	ID          string         `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	Status      string         `json:"status"`
	Blocks      []string       `json:"blocks"`
	BlockedBy   []string       `json:"blockedBy"`
	ActiveForm  string         `json:"activeForm,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// UnassignMemberTasks finds all tasks owned by memberName, clears their owner,
// sets status back to "pending", and returns the list of unassigned task IDs.
func (cm *teamConfigStore) UnassignMemberTasks(ctx context.Context, teamName, memberName string) ([]string, error) {
	if cm.taskLock != nil {
		cm.taskLock.Lock()
		defer cm.taskLock.Unlock()
	}
	tasksDir := tasksDirPath(cm.baseDir, teamName)
	files, err := cm.backend.LsInfo(ctx, &LsInfoRequest{Path: tasksDir})
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}

	var unassigned []string
	parseErrors := make([]plantask.TaskParseError, 0)
	for _, f := range files {
		taskPath := resolveListedPath(tasksDir, f.Path)
		name := filepath.Base(f.Path)
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		idStr := strings.TrimSuffix(name, ".json")
		if _, err := strconv.Atoi(idStr); err != nil {
			continue // skip non-numeric files like .highwatermark
		}

		content, err := cm.backend.Read(ctx, &ReadRequest{FilePath: taskPath})
		if err != nil {
			return nil, fmt.Errorf("read task %s: %w", taskPath, err)
		}
		if content == "" {
			continue
		}

		var t taskEntry
		if unmarshalErr := sonic.UnmarshalString(content, &t); unmarshalErr != nil {
			log.Printf("team config: parse task %s failed: %v", taskPath, unmarshalErr)
			parseErrors = append(parseErrors, plantask.TaskParseError{FilePath: taskPath, Err: unmarshalErr})
			continue
		}

		if t.Owner != memberName {
			continue
		}

		t.Owner = ""
		if t.Status == "in_progress" {
			t.Status = "pending"
		}

		data, err := sonic.MarshalString(t)
		if err != nil {
			return nil, fmt.Errorf("marshal task %s: %w", t.ID, err)
		}
		if err := cm.backend.Write(ctx, &WriteRequest{
			FilePath: taskPath,
			Content:  data,
		}); err != nil {
			return nil, fmt.Errorf("write task %s: %w", t.ID, err)
		}
		unassigned = append(unassigned, t.ID)
	}

	if len(parseErrors) > 0 {
		paths := make([]string, 0, len(parseErrors))
		for _, pe := range parseErrors {
			paths = append(paths, pe.FilePath)
		}
		log.Printf("team config: unassign member tasks parsed %d task(s) with errors: %s", len(parseErrors), strings.Join(paths, ", "))
		return unassigned, &plantask.TaskParseErrors{Items: parseErrors}
	}
	return unassigned, nil
}
