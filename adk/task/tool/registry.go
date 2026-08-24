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

package tool

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

// Registration binds a stable model-facing tool name to an implementation.
// Equivalent registrations must be installed on every Worker eligible to claim
// tasks for the selected executor key.
type Registration struct {
	// Info and Tool are required and snapshotted by Register.
	Info *schema.ToolInfo
	Tool Tool
	// Description formats persisted arguments for task presentation. Nil uses
	// the tool name. It may be called concurrently, must not panic, and must
	// return the same value when repeated with the same arguments.
	Description func(arguments string) string
	// RenderResult returns rich content for a successfully completed foreground
	// result. The framework prepends its text control envelope to the returned
	// parts. Nil embeds raw result bytes in that envelope. It may be called
	// concurrently; errors are returned to the invoking model call without
	// changing terminal task state.
	RenderResult func(context.Context, *background.TaskSnapshot) (*schema.ToolResult, error)
	// Materializer optionally derives an EventID-idempotent output file.
	Materializer OutputMaterializer
}

// Registry stores plain and recoverable registrations independently so a name
// may migrate between capability classes while old persisted tasks remain valid.
type Registry struct {
	mu          sync.RWMutex
	plain       map[string]*Registration
	recoverable map[string]*Registration
	projections *projectionRegistry
}

// NewRegistry creates an empty managed-tool registry.
func NewRegistry() *Registry {
	return &Registry{
		plain: make(map[string]*Registration), recoverable: make(map[string]*Registration),
		projections: newProjectionRegistry(),
	}
}

// Register adds a registration to the capability class implemented by Tool.
func (r *Registry) Register(registration *Registration) error {
	if r == nil || registration == nil || registration.Info == nil ||
		registration.Tool == nil {
		return errors.New("task/tool: registry, tool info, and implementation are required")
	}
	if registration.Info.Name == "" {
		return errors.New("task/tool: tool name is required")
	}
	info, err := cloneToolInfo(registration.Info)
	if err != nil {
		return fmt.Errorf("task/tool: clone tool info: %w", err)
	}
	target := r.plain
	if _, ok := registration.Tool.(RecoverableTool); ok {
		target = r.recoverable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := target[registration.Info.Name]; exists {
		return fmt.Errorf("%w: managed tool %q", background.ErrAlreadyExists, registration.Info.Name)
	}
	copy := *registration
	copy.Info = info
	target[info.Name] = &copy
	return nil
}

func (r *Registry) resolve(name string, recoverable bool) (*Registration, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	source := r.plain
	if recoverable {
		source = r.recoverable
	}
	registration, ok := source[name]
	if !ok {
		return nil, false
	}
	copy := *registration
	return &copy, true
}

func (r *Registry) resolveAny(name string) (*Registration, bool, bool) {
	if registration, ok := r.resolve(name, true); ok {
		return registration, true, true
	}
	registration, ok := r.resolve(name, false)
	return registration, false, ok
}
