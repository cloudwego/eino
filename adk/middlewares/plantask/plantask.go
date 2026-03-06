/*
 * Copyright 2025 CloudWeGo Authors
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

package plantask

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// Config is the configuration for the tool search middleware.
type Config struct {
	Backend Backend
	BaseDir string
}

// New creates a new plantask middleware that provides task management tools for agents.
// It adds TaskCreate, TaskGet, TaskUpdate, and TaskList tools to the agent's tool set,
// allowing agents to create and manage structured task lists during coding sessions.
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}

	return &middleware{backend: config.Backend, baseDir: config.BaseDir}, nil
}

type middleware struct {
	adk.BaseChatModelAgentMiddleware
	backend Backend
	baseDir string
}

func (m *middleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	lock := sync.Mutex{}
	nRunCtx.Tools = append(nRunCtx.Tools,
		newTaskCreateTool(m.backend, m.baseDir, &lock),
		newTaskGetTool(m.backend, m.baseDir, &lock),
		newTaskUpdateTool(m.backend, m.baseDir, &lock),
		newTaskListTool(m.backend, m.baseDir, &lock),
	)

	return ctx, &nRunCtx, nil
}
