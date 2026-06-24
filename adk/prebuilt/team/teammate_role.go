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

// teammate_role.go defines reusable teammate roles (TeammateRole) and the logic
// that turns a subagent_type chosen by the leader into a concrete teammate
// configuration.
//
// A TeammateRole plays the same two roles as a Claude subagent definition:
//
//   - Discovery: its Name + Description + tool summary are rendered into the
//     leader's instruction (see renderAvailableSubagentTypes) so the leader knows
//     which subagent_type values exist and when to use each. The Description is
//     only ever shown to the leader for selection; it never enters the teammate's
//     own context.
//   - Application: when the Agent tool spawns a teammate with that subagent_type,
//     the def's Model / Tools / Instruction are overlaid onto the leader's base
//     AgentConfig (see overlaySubagentConfig) to build the teammate. The
//     Instruction is appended to (not a replacement for) the base instruction.
//
// Team coordination tools (SendMessage, the Task* tools) are injected outside of
// AgentConfig.ToolsConfig.Tools — by teamMiddleware.BeforeAgent and the plantask
// middleware respectively — so a def's Tools allowlist only ever narrows the
// host-supplied business tools and never removes a teammate's ability to
// coordinate. This matches Claude's "SendMessage and task tools are always
// available even when tools restricts other tools".

package team

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// TeammateRole is a reusable teammate role. It is supplied by the host via
// RunnerConfig.TeammateRoles and referenced by the leader through the Agent tool's
// subagent_type parameter.
//
// Because the team package is a library (not a CLI that loads role definitions
// from markdown frontmatter), the Model and Tools are live Go objects rather
// than file references.
type TeammateRole struct {
	// Name is the role identifier and the value the leader passes as
	// subagent_type. Required and must be unique within RunnerConfig.TeammateRoles.
	// It is validated with the same rules as a team/member name.
	Name string

	// Description tells the leader when to choose this role. It is rendered into
	// the leader's instruction for selection and is NOT added to the teammate's
	// own context. Recommended but optional.
	Description string

	// Instruction is appended to the teammate's system prompt (after the base
	// AgentConfig.Instruction and the teammate-name line). Use it to specialize
	// the role's behavior. Optional.
	Instruction string

	// Model overrides the chat model for teammates of this type. Optional; when
	// nil the teammate inherits the leader's AgentConfig.Model.
	Model model.BaseModel[*schema.Message]

	// Tools is the allowlist of business tools available to teammates of this
	// type. Optional; when nil the teammate inherits the leader's configured
	// tools. SendMessage and the Task* tools are injected separately and remain
	// available regardless of this list.
	Tools []tool.BaseTool
}

// subagentRegistry indexes TeammateRole entries by Name for lookup during spawn
// and for rendering the available-types list. In production it is never empty:
// newSubagentRegistry injects a default general-purpose role when the host
// supplies none. An empty registry only arises when a lifecycleManager is built
// directly in a unit test (bypassing NewRunner), where subagent_type enforcement
// is then skipped.
type subagentRegistry struct {
	defs  map[string]TeammateRole
	order []string // preserves RunnerConfig.TeammateRoles order for stable rendering
}

// defaultRoleDescription / defaultRoleDescriptionChinese is the framework-supplied
// Description for the auto-injected general-purpose role (see newSubagentRegistry).
// The role itself supplies no Model/Tools/Instruction, so a general-purpose
// teammate is built from the leader's base config unchanged.
const defaultRoleDescription = "General-purpose teammate for any task that does not fit a more specialized role. Inherits the leader's model and tools."
const defaultRoleDescriptionChinese = "通用型队友，适用于不属于任何专门角色的任务。继承团队负责人的模型与工具配置。"

// newSubagentRegistry builds a registry from the host-supplied defs, validating
// each name and rejecting duplicates so a misconfiguration fails at Runner
// construction rather than at spawn time.
//
// When defs is empty the framework injects a single default general-purpose role
// (Name = generalAgentName). This guarantees the registry is never empty, so the
// "subagent_type is required and must match a declared role" rule (enforced by
// the Agent tool) always has at least one valid value — the leader can never spawn
// an unconstrained teammate. When the host supplies its own roles, the default is
// NOT added: the host's role set is treated as the exhaustive, intended allowlist.
func newSubagentRegistry(defs []TeammateRole) (*subagentRegistry, error) {
	if len(defs) == 0 {
		defs = []TeammateRole{{
			Name:        generalAgentName,
			Description: selectToolDesc(defaultRoleDescription, defaultRoleDescriptionChinese),
		}}
	}

	r := &subagentRegistry{defs: make(map[string]TeammateRole, len(defs))}
	for i, d := range defs {
		if err := validateName("subagent type", d.Name); err != nil {
			return nil, fmt.Errorf("subagent[%d]: %w", i, err)
		}
		if _, dup := r.defs[d.Name]; dup {
			return nil, fmt.Errorf("subagent[%d]: duplicate subagent type %q", i, d.Name)
		}
		r.defs[d.Name] = d
		r.order = append(r.order, d.Name)
	}
	return r, nil
}

// empty reports whether no subagent roles are configured.
func (r *subagentRegistry) empty() bool {
	return r == nil || len(r.defs) == 0
}

// lookup returns the def for the given type and whether it exists.
func (r *subagentRegistry) lookup(subagentType string) (TeammateRole, bool) {
	if r == nil {
		return TeammateRole{}, false
	}
	d, ok := r.defs[subagentType]
	return d, ok
}

// resolve validates a subagent_type chosen by the leader and returns its def.
//
// subagent_type is required and must match a declared role:
//   - non-empty registry + empty subagentType → error (required). The leader must
//     pick a role; with the auto-injected general-purpose default there is always
//     at least one valid choice.
//   - non-empty registry + unknown type → error listing the valid types.
//   - non-empty registry + known type → (def, true, nil): overlay it.
//   - empty/nil registry → (zero def, false, nil): no enforcement. This only
//     happens when a lifecycleManager is constructed directly in a unit test
//     (NewRunner always builds a non-empty registry), so it is a degrade-to-label
//     escape hatch, not a production path.
//
// On error the Agent tool surfaces the message back to the model, which retries
// with a valid type on its next turn.
func (r *subagentRegistry) resolve(subagentType string) (TeammateRole, bool, error) {
	if r.empty() {
		return TeammateRole{}, false, nil
	}
	if subagentType == "" {
		return TeammateRole{}, false, fmt.Errorf(
			"subagent_type is required; choose one of: %s", strings.Join(r.order, ", "))
	}
	d, ok := r.lookup(subagentType)
	if !ok {
		return TeammateRole{}, false, fmt.Errorf(
			"unknown subagent_type %q; valid types: %s",
			subagentType, strings.Join(r.order, ", "))
	}
	return d, true, nil
}

// overlaySubagentConfig returns a copy of base with the def's Model, Tools, and
// Instruction overlaid. base is copied by value first so the caller's config is
// never mutated; the nested ToolsConfig is also overwritten as a whole only when
// the def supplies tools, leaving the inherited ToolsConfig (ReturnDirectly,
// EmitInternalEvents, etc.) intact otherwise.
//
// The teammate-name line and the shared teammate instruction are layered on by
// buildTeammateAgent via extraInstruction; this function only folds in the
// role's own Instruction so the final order is:
//
//	base.Instruction → def.Instruction → ("Your agent name is ..." + teammate instruction)
func overlaySubagentConfig(base *adk.ChatModelAgentConfig, def TeammateRole) *adk.ChatModelAgentConfig {
	cfg := *base

	if def.Model != nil {
		cfg.Model = def.Model
	}
	if def.Tools != nil {
		tc := base.ToolsConfig
		tc.Tools = def.Tools
		cfg.ToolsConfig = tc
	}
	if def.Instruction != "" {
		if cfg.Instruction == "" {
			cfg.Instruction = def.Instruction
		} else {
			cfg.Instruction = cfg.Instruction + "\n" + def.Instruction
		}
	}

	return &cfg
}

// renderAvailableSubagentTypes renders the registry into a block appended to the
// leader's instruction so the model knows which subagent_type values exist and
// when to use each (Claude's system-reminder "available agent types" role A). It
// returns "" only for an empty registry (a direct-construction unit-test path);
// in production the registry always has at least the default general-purpose role.
//
// Names and descriptions are passed through escapeBraces because the leader
// instruction is run through f-string placeholder substitution (see
// ChatModelAgentConfig.Instruction); a literal "{" in a def would otherwise be
// misread as a placeholder.
func renderAvailableSubagentTypes(r *subagentRegistry) string {
	if r.empty() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Available agent types for the Agent tool\n\n")
	sb.WriteString("When spawning a teammate with the Agent tool, set subagent_type to one of:\n")
	for _, name := range r.order {
		d := r.defs[name]
		sb.WriteString("- ")
		sb.WriteString(escapeBraces(d.Name))
		if d.Description != "" {
			sb.WriteString(": ")
			sb.WriteString(escapeBraces(d.Description))
		}
		if summary := toolNamesSummary(d.Tools); summary != "" {
			sb.WriteString(" (Tools: ")
			sb.WriteString(summary)
			sb.WriteString(")")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nsubagent_type is required and must be exactly one of the names above.")
	return sb.String()
}

// toolNamesSummary returns a comma-separated list of the def's tool names for
// the available-types block. It returns "" when the def inherits the leader's
// tools (nil) so the rendered line does not imply a restriction that is not
// there. A tool whose Info cannot be read is listed as "<tool>" rather than
// dropped, so the count still reflects reality.
func toolNamesSummary(tools []tool.BaseTool) string {
	if tools == nil {
		return ""
	}
	if len(tools) == 0 {
		return "none"
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		name := "<tool>"
		if info, err := t.Info(context.Background()); err == nil && info != nil && info.Name != "" {
			name = info.Name
		}
		names = append(names, escapeBraces(name))
	}
	return strings.Join(names, ", ")
}

// escapeBraces doubles curly braces so text embedded in the leader instruction
// is not interpreted as an f-string placeholder during model-input generation.
func escapeBraces(s string) string {
	s = strings.ReplaceAll(s, "{", "{{")
	return strings.ReplaceAll(s, "}", "}}")
}
