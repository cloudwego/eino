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
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/plantask"
	"github.com/cloudwego/eino/components/tool"
)

// Config is the configuration for the team middleware.
type Config struct {
	// Backend is the storage backend for team data.
	Backend Backend

	// BaseDir is the root directory for team data storage.
	// All team files (config, inboxes, tasks) are stored under this directory.
	BaseDir string

	// InboxLocks is the shared lock manager for inbox file access.
	// Initialized automatically by NewRunner if nil.
	InboxLocks *inboxLockManager
}

func newTeamLeadMiddleware(conf *RunnerConfig) *teamMiddleware {
	return newMiddleware(conf, true, LeaderAgentName)
}

func newTeamTeammateMiddleware(conf *RunnerConfig, agentName, teamName string) *teamMiddleware {
	mw := newMiddleware(conf, false, agentName)
	mw.teamName = teamName
	return mw
}

// newMiddleware creates a new team middleware.
func newMiddleware(conf *RunnerConfig, isLeader bool, agentName string) *teamMiddleware {
	return &teamMiddleware{
		conf:      conf,
		teammates: &sync.Map{},
		isLeader:  isLeader,
		agentName: agentName,
	}
}

type teamMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	conf      *RunnerConfig
	teammates *sync.Map // name → *spawnResult
	isLeader  bool
	agentName string
	teamName  string              // set at creation for teammates; set by TeamCreate for leader
	router    *sourceRouter       // set by NewRunner, used by agentTool for teammate sources
	ptMW      plantask.Middleware // plantask middleware instance, used for locked task operations
}

// BeforeAgent injects team tools before each agent run.
func (mw *teamMiddleware) BeforeAgent(ctx context.Context,
	runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {

	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	var tools []tool.BaseTool

	if mw.isLeader {
		tools = append(tools,
			newTeamCreateTool(mw),
			newTeamDeleteTool(mw),
			newAgentTool(mw),
		)
	}

	// SendMessage is available to both Leader and Teammate
	sendMsgTool, err := newSendMessageTool(mw, mw.agentName)
	if err != nil {
		return ctx, nil, err
	}
	tools = append(tools, sendMsgTool)

	nRunCtx.Tools = append(nRunCtx.Tools, tools...)
	return ctx, &nRunCtx, nil
}

// ShutdownAllTeammates cancels all active teammates and deletes their _internal shadow tasks.
func (mw *teamMiddleware) ShutdownAllTeammates(ctx context.Context, teamName string) {
	mw.teammates.Range(func(key, value any) bool {
		name, _ := key.(string)
		if name != "" {
			mw.cleanupExitedTeammate(ctx, teamName, name)
		}
		return true
	})
}
