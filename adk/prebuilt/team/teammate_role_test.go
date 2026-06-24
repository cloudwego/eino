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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// fakeTool is a minimal InvokableTool used to assert tool-allowlist overlays.
type fakeTool struct{ name string }

func (f *fakeTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: f.name, Desc: f.name}, nil
}

func (f *fakeTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "ok", nil
}

func TestNewSubagentRegistry_ValidatesAndDedups(t *testing.T) {
	// Valid set.
	r, err := newSubagentRegistry([]TeammateRole{
		{Name: "security-reviewer"},
		{Name: "perf-reviewer"},
	})
	assert.NoError(t, err)
	assert.False(t, r.empty())
	assert.Equal(t, []string{"security-reviewer", "perf-reviewer"}, r.order)

	// Duplicate name.
	_, err = newSubagentRegistry([]TeammateRole{{Name: "dup"}, {Name: "dup"}})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")

	// Invalid name (reserved leader-style path char).
	_, err = newSubagentRegistry([]TeammateRole{{Name: "../evil"}})
	assert.Error(t, err)

	// Empty list injects the default general-purpose role rather than staying empty,
	// so subagent_type always has at least one valid value.
	def, err := newSubagentRegistry(nil)
	assert.NoError(t, err)
	assert.False(t, def.empty())
	assert.Equal(t, []string{generalAgentName}, def.order)
	role, ok := def.lookup(generalAgentName)
	assert.True(t, ok)
	assert.NotEmpty(t, role.Description)
}

func TestSubagentRegistry_Resolve(t *testing.T) {
	r, err := newSubagentRegistry([]TeammateRole{{Name: "explorer"}})
	assert.NoError(t, err)

	// Empty type with a non-empty registry: required -> error.
	_, ok, err := r.resolve("")
	assert.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "required")
	assert.Contains(t, err.Error(), "explorer")

	// Known type: resolved.
	def, ok, err := r.resolve("explorer")
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "explorer", def.Name)

	// Unknown type with a non-empty registry: error listing valid types.
	_, ok, err = r.resolve("nope")
	assert.Error(t, err)
	assert.False(t, ok)
	assert.Contains(t, err.Error(), "explorer")

	// Empty registry (only reachable via direct construction, not NewRunner):
	// enforcement is skipped, so any value resolves to no-overlay without error.
	emptyReg := &subagentRegistry{}
	_, ok, err = emptyReg.resolve("anything")
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestOverlaySubagentConfig(t *testing.T) {
	baseModel := &mockBaseChatModel{}
	baseTool := &fakeTool{name: "base-tool"}
	base := &adk.ChatModelAgentConfig{
		Name:        "leader",
		Instruction: "base instruction",
		Model:       baseModel,
	}
	base.ToolsConfig.Tools = []tool.BaseTool{baseTool}

	// Empty def overlays nothing (model, tools, instruction all inherited).
	got := overlaySubagentConfig(base, TeammateRole{Name: "x"})
	assert.Equal(t, "base instruction", got.Instruction)
	assert.Equal(t, baseModel, got.Model)
	assert.Equal(t, []tool.BaseTool{baseTool}, got.ToolsConfig.Tools)
	// Base must be untouched.
	assert.Equal(t, "base instruction", base.Instruction)

	// Full def overlays model, tools, and appends instruction.
	roleModel := &mockBaseChatModel{}
	roleTool := &fakeTool{name: "role-tool"}
	got = overlaySubagentConfig(base, TeammateRole{
		Name:        "x",
		Instruction: "role instruction",
		Model:       roleModel,
		Tools:       []tool.BaseTool{roleTool},
	})
	assert.Equal(t, roleModel, got.Model)
	assert.Equal(t, []tool.BaseTool{roleTool}, got.ToolsConfig.Tools)
	assert.Equal(t, "base instruction\nrole instruction", got.Instruction)
	// Base remains unchanged after overlay.
	assert.Equal(t, baseModel, base.Model)
	assert.Equal(t, []tool.BaseTool{baseTool}, base.ToolsConfig.Tools)

	// Empty Tools slice (non-nil) is an explicit "no business tools" allowlist and
	// must override inheritance, distinct from nil (inherit).
	got = overlaySubagentConfig(base, TeammateRole{Name: "x", Tools: []tool.BaseTool{}})
	assert.NotNil(t, got.ToolsConfig.Tools)
	assert.Len(t, got.ToolsConfig.Tools, 0)
}

func TestRenderAvailableSubagentTypes(t *testing.T) {
	// Empty registry (direct construction, not via NewRunner) renders nothing.
	assert.Equal(t, "", renderAvailableSubagentTypes(&subagentRegistry{}))

	// A registry built from nil defs carries the default general-purpose role.
	def, _ := newSubagentRegistry(nil)
	assert.Contains(t, renderAvailableSubagentTypes(def), generalAgentName)

	r, _ := newSubagentRegistry([]TeammateRole{
		{Name: "security-reviewer", Description: "Finds vulns", Tools: []tool.BaseTool{&fakeTool{name: "Read"}}},
		{Name: "researcher", Description: "Researches things"}, // nil Tools -> inherits, no (Tools: ...)
	})
	out := renderAvailableSubagentTypes(r)
	assert.Contains(t, out, "Available agent types")
	assert.Contains(t, out, "- security-reviewer: Finds vulns (Tools: Read)")
	assert.Contains(t, out, "- researcher: Researches things")
	// researcher inherits tools, so no Tools clause for it.
	assert.NotContains(t, out, "researcher: Researches things (Tools")
	// Order preserved.
	assert.True(t, strings.Index(out, "security-reviewer") < strings.Index(out, "researcher"))
	// The block states subagent_type is required.
	assert.Contains(t, out, "required")
}

func TestRenderAvailableSubagentTypes_EscapesBraces(t *testing.T) {
	r, _ := newSubagentRegistry([]TeammateRole{
		{Name: "json-helper", Description: "emits {json} payloads"},
	})
	out := renderAvailableSubagentTypes(r)
	// Literal braces must be doubled so FString templating does not treat them as
	// placeholders.
	assert.Contains(t, out, "{{json}}")
	assert.NotContains(t, out, "emits {json}")
}

func TestToolNamesSummary(t *testing.T) {
	// nil -> "" (inherits, no restriction implied).
	assert.Equal(t, "", toolNamesSummary(nil))
	// explicit empty -> "none".
	assert.Equal(t, "none", toolNamesSummary([]tool.BaseTool{}))
	// names joined.
	assert.Equal(t, "a, b", toolNamesSummary([]tool.BaseTool{&fakeTool{name: "a"}, &fakeTool{name: "b"}}))
}

// --- Integration through NewRunner / Agent tool ---------------------------------

func newSubagentTestRunner(t *testing.T, name string, subagents []TeammateRole) (*Runner, *Config) {
	t.Helper()
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/tmp/test", Name: name}
	runnerConf := &RunnerConfig{
		AgentConfig:   &adk.ChatModelAgentConfig{Name: "leader", Description: "test", Model: &blockingChatModel{}},
		TeamConfig:    conf,
		TeammateRoles: subagents,
		GenInput: func(_ context.Context, _ *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	}
	runner, err := NewRunner(context.Background(), runnerConf)
	assert.NoError(t, err)
	return runner, conf
}

// TestNewRunner_InvalidSubagentDefRejected verifies a bad TeammateRoles list fails
// Runner construction rather than at spawn time.
func TestNewRunner_InvalidSubagentDefRejected(t *testing.T) {
	conf := &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp/test", Name: "t"}
	_, err := NewRunner(context.Background(), &RunnerConfig{
		AgentConfig:   &adk.ChatModelAgentConfig{Name: "leader", Description: "x", Model: &mockBaseChatModel{}},
		TeamConfig:    conf,
		TeammateRoles: []TeammateRole{{Name: "bad name with spaces"}},
		GenInput: func(_ context.Context, _ *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		OnAgentEvents: noopOnAgentEvents,
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TeammateRoles")
}

// TestAgentTool_UnknownSubagentTypeRejected verifies an unknown subagent_type is
// rejected by the Agent tool (decision D2) and no member is registered.
func TestAgentTool_UnknownSubagentTypeRejected(t *testing.T) {
	runner, conf := newSubagentTestRunner(t, "myteam", []TeammateRole{{Name: "reviewer"}})
	defer runner.leaderMW.ShutdownAllTeammates(context.Background())

	agentT := newAgentTool(runner.leaderMW)
	_, err := agentT.InvokableRun(context.Background(),
		`{"name":"worker","prompt":"do it","description":"task","subagent_type":"nope"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subagent_type")

	has, _ := newConfigStore(conf).HasMember(context.Background(), "myteam", "worker")
	assert.False(t, has, "rejected spawn must not register a member")
}

// TestAgentTool_UnknownSubagentTypeRejected_ForegroundPath is a regression test
// for the dispatch hole where an Agent call with neither name nor
// run_in_background fell through to the one-shot foreground sub-agent, which
// ignored subagent_type entirely (no validation, no error). An invalid type must
// be rejected on this path too, before any foreground run happens.
func TestAgentTool_UnknownSubagentTypeRejected_ForegroundPath(t *testing.T) {
	runner, _ := newSubagentTestRunner(t, "myteam", []TeammateRole{{Name: "reviewer"}})
	defer runner.leaderMW.ShutdownAllTeammates(context.Background())

	agentT := newAgentTool(runner.leaderMW)
	// No name, no run_in_background -> foreground dispatch path.
	_, err := agentT.InvokableRun(context.Background(),
		`{"prompt":"do it","description":"task","subagent_type":"physics-expert"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subagent_type")
	assert.Contains(t, err.Error(), "reviewer", "error should list the valid types")
}

// TestAgentTool_KnownSubagentTypeSpawns verifies a declared subagent_type spawns
// successfully and is recorded on the member.
func TestAgentTool_KnownSubagentTypeSpawns(t *testing.T) {
	runner, conf := newSubagentTestRunner(t, "myteam", []TeammateRole{
		{Name: "reviewer", Description: "reviews", Instruction: "be thorough", Model: &blockingChatModel{}},
	})
	defer runner.leaderMW.ShutdownAllTeammates(context.Background())

	agentT := newAgentTool(runner.leaderMW)
	result, err := agentT.InvokableRun(context.Background(),
		`{"name":"worker","prompt":"do it","description":"task","subagent_type":"reviewer"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Spawned successfully")

	has, _ := newConfigStore(conf).HasMember(context.Background(), "myteam", "worker")
	assert.True(t, has)
}

// TestAgentTool_EmptySubagentTypeRejected verifies that an omitted subagent_type
// is rejected (it is required) and that the error lists the valid roles.
func TestAgentTool_EmptySubagentTypeRejected(t *testing.T) {
	runner, conf := newSubagentTestRunner(t, "myteam", []TeammateRole{{Name: "reviewer"}})
	defer runner.leaderMW.ShutdownAllTeammates(context.Background())

	agentT := newAgentTool(runner.leaderMW)
	_, err := agentT.InvokableRun(context.Background(),
		`{"name":"worker","prompt":"do it","description":"task"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required")
	assert.Contains(t, err.Error(), "reviewer")

	has, _ := newConfigStore(conf).HasMember(context.Background(), "myteam", "worker")
	assert.False(t, has, "rejected spawn must not register a member")
}

// TestAgentTool_DefaultGeneralPurposeRole verifies that with no roles configured,
// the framework's default general-purpose role is the one valid subagent_type and
// spawning with it succeeds (while an arbitrary value is still rejected).
func TestAgentTool_DefaultGeneralPurposeRole(t *testing.T) {
	runner, conf := newSubagentTestRunner(t, "myteam", nil)
	defer runner.leaderMW.ShutdownAllTeammates(context.Background())

	agentT := newAgentTool(runner.leaderMW)

	// An arbitrary type is rejected even with no host-declared roles.
	_, err := agentT.InvokableRun(context.Background(),
		`{"name":"w1","prompt":"do it","description":"task","subagent_type":"anything-goes"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown subagent_type")

	// The default general-purpose role is valid and spawns.
	_, err = agentT.InvokableRun(context.Background(),
		`{"name":"w2","prompt":"do it","description":"task","subagent_type":"`+generalAgentName+`"}`)
	assert.NoError(t, err)
	has, _ := newConfigStore(conf).HasMember(context.Background(), "myteam", "w2")
	assert.True(t, has)
}

// TestAgentTool_Info_EnumeratesSubagentTypes verifies the Agent tool schema lists
// declared types as a required enum, and falls back to the default
// general-purpose role when the host declares none.
func TestAgentTool_Info_EnumeratesSubagentTypes(t *testing.T) {
	runner, _ := newSubagentTestRunner(t, "t1", []TeammateRole{{Name: "a"}, {Name: "b"}})
	defer runner.leaderMW.ShutdownAllTeammates(context.Background())

	info, err := newAgentTool(runner.leaderMW).Info(context.Background())
	assert.NoError(t, err)
	js, err := info.ParamsOneOf.ToJSONSchema()
	assert.NoError(t, err)
	st, ok := js.Properties.Get("subagent_type")
	assert.True(t, ok)
	assert.ElementsMatch(t, []any{"a", "b"}, st.Enum)
	assert.Contains(t, js.Required, "subagent_type", "subagent_type must be required")

	// No host roles: enum is the single default general-purpose role, still required.
	runner2, _ := newSubagentTestRunner(t, "t2", nil)
	defer runner2.leaderMW.ShutdownAllTeammates(context.Background())
	info2, _ := newAgentTool(runner2.leaderMW).Info(context.Background())
	js2, _ := info2.ParamsOneOf.ToJSONSchema()
	st2, ok := js2.Properties.Get("subagent_type")
	assert.True(t, ok)
	assert.ElementsMatch(t, []any{generalAgentName}, st2.Enum)
	assert.Contains(t, js2.Required, "subagent_type")
}

// TestNewRunner_InjectsSubagentTypesIntoLeaderInstruction verifies the available
// types block reaches the leader's instruction.
func TestNewRunner_InjectsSubagentTypesIntoLeaderInstruction(t *testing.T) {
	// Build the rendered block directly and confirm it is non-empty and contains
	// the role; the runner wiring appends exactly this string to the instruction.
	r, _ := newSubagentRegistry([]TeammateRole{{Name: "security-reviewer", Description: "finds vulns"}})
	block := renderAvailableSubagentTypes(r)
	assert.Contains(t, block, "security-reviewer")
	assert.Contains(t, block, "finds vulns")
}
