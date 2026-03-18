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
	"path/filepath"
	"strings"
)

func (mw *teamMiddleware) configStore() *teamConfigStore {
	return newTeamConfigStoreFromConfig(mw.conf.TeamConfig)
}

func (mw *teamMiddleware) mailbox(teamName, ownerName string) *mailbox {
	return newMailboxFromConfig(mw.conf.TeamConfig, teamName, ownerName)
}

func (mw *teamMiddleware) initInbox(ctx context.Context, teamName, ownerName string) error {
	return initInboxFile(ctx, mw.conf.TeamConfig.Backend, mw.inboxPath(teamName, ownerName))
}

func (mw *teamMiddleware) inboxPath(teamName, agentName string) string {
	return filepath.Join(inboxDirPath(mw.conf.TeamConfig.BaseDir, teamName), agentName+".json")
}

func (mw *teamMiddleware) registerTeammateRuntime(name string, result *spawnResult) {
	mw.teammates.Store(name, result)
}

func (mw *teamMiddleware) startTeammateRunner(parentCtx context.Context,
	teamName, memberName string, result *spawnResult, run func(context.Context) error) {

	mw.registerTeammateRuntime(memberName, result)

	safeGo(func() {
		defer mw.cleanupExitedTeammate(context.Background(), teamName, memberName)
		_ = run(parentCtx)
	})
}

func (mw *teamMiddleware) cleanupFailedTeammateSpawn(ctx context.Context, teamName, memberName string) {
	_ = mw.configStore().RemoveMember(ctx, teamName, memberName)
	_ = mw.conf.TeamConfig.Backend.Delete(ctx, &DeleteRequest{FilePath: mw.inboxPath(teamName, memberName)})
	if mw.router != nil {
		mw.router.UnsetMailbox(memberName)
	}
}

func (mw *teamMiddleware) stopTeammateRuntime(ctx context.Context, teamName, memberName string) {
	if val, ok := mw.teammates.LoadAndDelete(memberName); ok {
		result := val.(*spawnResult)
		if result.Cancel != nil {
			result.Cancel()
		}
		if result.LocalTaskID != "" && mw.ptMW != nil {
			_ = mw.ptMW.DeleteTask(ctx, result.LocalTaskID)
		}
	}
	if mw.router != nil {
		mw.router.UnsetMailbox(memberName)
	}
}

func (mw *teamMiddleware) cleanupExitedTeammate(ctx context.Context, teamName, memberName string) {
	cm := mw.configStore()
	_, _ = cm.UnassignMemberTasks(ctx, teamName, memberName)
	_ = cm.RemoveMember(ctx, teamName, memberName)
	mw.stopTeammateRuntime(ctx, teamName, memberName)
}

func (mw *teamMiddleware) removeTeammate(ctx context.Context, teamName, memberName string) ([]string, error) {
	cm := mw.configStore()

	// Stop the runtime first so a failed cleanup does not leave a live teammate
	// that is no longer reachable through the team config.
	mw.stopTeammateRuntime(ctx, teamName, memberName)

	unassigned, err := cm.UnassignMemberTasks(ctx, teamName, memberName)
	if err != nil {
		return nil, fmt.Errorf("unassign tasks for %q: %w", memberName, err)
	}

	if err := cm.RemoveMember(ctx, teamName, memberName); err != nil {
		return nil, fmt.Errorf("remove member %q: %w", memberName, err)
	}

	return unassigned, nil
}

func (mw *teamMiddleware) buildTeammateTerminationMessage(name string, unassigned []string) string {
	msg := fmt.Sprintf("%s has shut down.", name)
	if len(unassigned) > 0 {
		msg += fmt.Sprintf(" %d task(s) were unassigned: #%s.", len(unassigned), strings.Join(unassigned, ", #"))
	}
	return msg
}
