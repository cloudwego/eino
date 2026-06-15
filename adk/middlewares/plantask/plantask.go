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
	"github.com/cloudwego/eino/schema"
)

// Config is the configuration for the tool search middleware.
type Config struct {
	Backend Backend
	BaseDir string
}

// NewTyped creates a new plantask middleware that provides task management tools for agents.
// It adds TaskCreate, TaskGet, TaskUpdate, and TaskList tools to the agent's tool set,
// allowing agents to create and manage structured task lists during coding sessions.
//
// This is the generic constructor that supports both *schema.Message and *schema.AgenticMessage.
func NewTyped[M adk.MessageType](_ context.Context, config *Config) (adk.TypedChatModelAgentMiddleware[M], error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}

	return &typedMiddleware[M]{backend: config.Backend, baseDir: config.BaseDir}, nil
}

// New creates a new plantask middleware that provides task management tools for agents.
// It adds TaskCreate, TaskGet, TaskUpdate, and TaskList tools to the agent's tool set,
// allowing agents to create and manage structured task lists during coding sessions.
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped[*schema.Message](ctx, config)
}

type typedMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	backend Backend
	baseDir string
}

func (m *typedMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
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
