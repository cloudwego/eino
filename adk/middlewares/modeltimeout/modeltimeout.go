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

// Package modeltimeout provides ChatModelAgent middleware for enforcing model
// call and stream timeouts.
package modeltimeout

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Middleware wraps model calls with timeout enforcement.
type Middleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	config *Config
}

// New creates timeout middleware for the default *schema.Message ChatModelAgent.
func New(config *Config) adk.ChatModelAgentMiddleware {
	return NewTyped[*schema.Message](config)
}

// NewTyped creates timeout middleware for a typed ChatModelAgent.
func NewTyped[M adk.MessageType](config *Config) adk.TypedChatModelAgentMiddleware[M] {
	return &Middleware[M]{
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
		config:                            config,
	}
}

// WrapModel installs timeout enforcement around the next model.
func (m *Middleware[M]) WrapModel(_ context.Context, next model.BaseModel[M], _ *adk.TypedModelContext[M]) (model.BaseModel[M], error) {
	if !IsConfigActive(m.config) {
		return next, nil
	}
	return NewTypedTimeoutModelWrapper(next, m.config), nil
}
