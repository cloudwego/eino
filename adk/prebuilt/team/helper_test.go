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

	"github.com/cloudwego/eino/adk"
)

// newTestTeamMiddleware builds a leader teamMiddleware backed by an in-memory
// backend, without creating a team. Tests that need an active team call
// newConfigStore(conf).CreateTeam(...) and mw.setTeamName(...) themselves, which
// mirrors what NewRunner/setupTeam now do in production.
func newTestTeamMiddleware() (*teamMiddleware, *Config) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test"}
	conf.ensureInit()

	runnerConf := &RunnerConfig{
		TeamConfig:  conf,
		AgentConfig: &adk.ChatModelAgentConfig{Name: "test", Description: "test"},
	}

	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pumpMgr := newPumpManager(router, nopLogger{})
	mw := newTeamLeadMiddleware(runnerConf, router, pumpMgr)
	return mw, conf
}

// setupTestTeam creates a team for mw (directory layout + config.json with the
// leader as the first member), registers the leader's inbox, and sets the team
// active on the middleware, replicating the observable effect the removed
// TeamCreate tool used to have. It does NOT start the leader's mailbox pump, so
// tests that only inspect inbox files or membership stay deterministic. teamName
// must be a valid team name.
func setupTestTeam(ctx context.Context, mw *teamMiddleware, teamName string) error {
	if _, err := mw.lifecycle.createTeam(ctx, teamName, "", LeaderAgentName, ""); err != nil {
		return err
	}
	if err := mw.lifecycle.registerMailbox(ctx, teamName, LeaderAgentName, &mailboxSourceConfig{
		OwnerName: LeaderAgentName,
		Role:      teamRoleLeader,
	}); err != nil {
		return err
	}
	mw.setTeamName(teamName)
	return nil
}
