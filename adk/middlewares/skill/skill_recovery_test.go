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

package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type recoveryBackend struct {
	get func(context.Context, string) (Skill, error)
}

func (b *recoveryBackend) List(context.Context) ([]FrontMatter, error) {
	return []FrontMatter{{Name: "good", Description: "A test skill"}}, nil
}

func (b *recoveryBackend) Get(ctx context.Context, name string) (Skill, error) {
	if b.get != nil {
		return b.get(ctx, name)
	}
	if name != "good" {
		return Skill{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	return Skill{FrontMatter: FrontMatter{Name: "good"}, Content: "valid skill content"}, nil
}

func newTypedRecoveryTool[M adk.MessageType](t *testing.T, cfg *TypedConfig[M]) tool.InvokableTool {
	t.Helper()
	h, err := NewTyped(context.Background(), cfg)
	require.NoError(t, err)
	_, runCtx, err := h.BeforeAgent(context.Background(), &adk.TypedChatModelAgentContext[M]{})
	require.NoError(t, err)
	require.Len(t, runCtx.Tools, 1)
	return runCtx.Tools[0].(tool.InvokableTool)
}

func TestSkillInputRecoveryConstructors(t *testing.T) {
	constructors := map[string]func(*testing.T, *Config) tool.InvokableTool{
		"legacy": func(t *testing.T, cfg *Config) tool.InvokableTool {
			mw, err := New(context.Background(), cfg)
			require.NoError(t, err)
			return mw.AdditionalTools[0].(tool.InvokableTool)
		},
		"middleware": func(t *testing.T, cfg *Config) tool.InvokableTool {
			h, err := NewMiddleware(context.Background(), cfg)
			require.NoError(t, err)
			_, runCtx, err := h.BeforeAgent(context.Background(), &adk.ChatModelAgentContext{})
			require.NoError(t, err)
			return runCtx.Tools[0].(tool.InvokableTool)
		},
		"typed_message": func(t *testing.T, cfg *Config) tool.InvokableTool {
			return newTypedRecoveryTool(t, cfg)
		},
		"typed_agentic": func(t *testing.T, cfg *Config) tool.InvokableTool {
			return newTypedRecoveryTool(t, &TypedConfig[*schema.AgenticMessage]{
				Backend: cfg.Backend, RecoverInputErrors: cfg.RecoverInputErrors,
			})
		},
	}
	cases := []struct {
		name string
		args string
	}{
		{"missing", `{"skill":"missing"}`},
		{"invalid_json", `{`},
		{"invalid_type", `{"skill":3}`},
		{"missing_field", `{}`},
		{"valid", `{"skill":"good"}`},
	}
	for name, constructor := range constructors {
		t.Run(name, func(t *testing.T) {
			for _, enabled := range []bool{false, true} {
				for _, tc := range cases {
					t.Run(fmt.Sprintf("%s/recover=%t", tc.name, enabled), func(t *testing.T) {
						it := constructor(t, &Config{Backend: &recoveryBackend{}, RecoverInputErrors: enabled})
						out, err := it.InvokableRun(context.Background(), tc.args)
						if tc.name == "valid" {
							require.NoError(t, err)
							require.Contains(t, out, "valid skill content")
							return
						}
						if enabled {
							require.NoError(t, err)
							require.Contains(t, out, "[skill error]")
							require.Contains(t, out, "Check the skill name and arguments, then retry.")
							return
						}
						require.Error(t, err)
						require.Empty(t, out)
						switch tc.name {
						case "missing", "missing_field":
							require.ErrorIs(t, err, ErrSkillNotFound)
							if tc.name == "missing" {
								require.EqualError(t, err, "failed to get skill: skill not found: missing")
							}
						case "invalid_json":
							var syntaxErr *json.SyntaxError
							require.ErrorAs(t, err, &syntaxErr)
						case "invalid_type":
							var typeErr *json.UnmarshalTypeError
							require.ErrorAs(t, err, &typeErr)
						}
					})
				}
			}
		})
	}
}

func TestSkillInputRecoveryBackendFailures(t *testing.T) {
	for _, backendErr := range []error{
		errRecoveryBackendUnavailable,
		errors.New("skill not found: missing"),
		&json.SyntaxError{},
	} {
		it := newTypedRecoveryTool(t, &Config{
			Backend: &errorBackend{getErr: backendErr}, RecoverInputErrors: true,
		})
		out, err := it.InvokableRun(context.Background(), `{"skill":"good"}`)
		require.Empty(t, out)
		require.ErrorIs(t, err, backendErr)
	}
}

var errRecoveryBackendUnavailable = errors.New("backend unavailable")

// This error deliberately has both classifications to verify control precedence.
type notFoundControlError struct{ cause error }

func (e notFoundControlError) Error() string        { return e.cause.Error() }
func (e notFoundControlError) Unwrap() error        { return e.cause }
func (e notFoundControlError) Is(target error) bool { return target == ErrSkillNotFound }

type recoveryCheckpointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *recoveryCheckpointStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[id]
	return append([]byte(nil), b...), ok, nil
}

func (s *recoveryCheckpointStore) Set(_ context.Context, id string, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[id] = append([]byte(nil), b...)
	return nil
}

func recoveryGraphInterrupt(t *testing.T) error {
	t.Helper()
	g := compose.NewGraph[string, string]()
	require.NoError(t, g.AddLambdaNode("approval", compose.InvokableLambda(func(ctx context.Context, _ string) (string, error) {
		return "", tool.Interrupt(ctx, "approval required")
	})))
	require.NoError(t, g.AddEdge(compose.START, "approval"))
	require.NoError(t, g.AddEdge("approval", compose.END))
	r, err := g.Compile(context.Background(), compose.WithCheckPointStore(&recoveryCheckpointStore{data: make(map[string][]byte)}))
	require.NoError(t, err)
	_, err = r.Invoke(context.Background(), "", compose.WithCheckPointID("approval"))
	_, interrupted := compose.ExtractInterruptInfo(err)
	require.True(t, interrupted)
	return err
}

func TestSkillInputRecoveryControlSignals(t *testing.T) {
	ctx := context.Background()
	cases := map[string]error{
		"canceled":         context.Canceled,
		"deadline":         context.DeadlineExceeded,
		"stream_canceled":  adk.ErrStreamCanceled,
		"agent_canceled":   &adk.CancelError{Info: &adk.AgentCancelInfo{}},
		"tool_interrupt":   tool.Interrupt(ctx, "approval required"),
		"graph_interrupt":  recoveryGraphInterrupt(t),
		"legacy_interrupt": compose.NewInterruptAndRerunErr("approval required"),
	}
	for name, signal := range cases {
		t.Run(name, func(t *testing.T) {
			for _, marked := range []bool{false, true} {
				lookupErr := signal
				if marked {
					lookupErr = notFoundControlError{cause: signal}
				}
				it := newTypedRecoveryTool(t, &Config{
					Backend: &errorBackend{getErr: fmt.Errorf("lookup: %w", lookupErr)}, RecoverInputErrors: true,
				})
				out, err := it.InvokableRun(ctx, `{"skill":"good"}`)
				require.Empty(t, out)
				require.ErrorIs(t, err, signal)
			}
		})
	}
}

func TestSkillInputRecoveryCanceledContext(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	for _, ctx := range []context.Context{canceled, expired} {
		for _, args := range []string{`{`, `{"skill":"missing"}`} {
			it := newTypedRecoveryTool(t, &Config{Backend: &recoveryBackend{}, RecoverInputErrors: true})
			out, err := it.InvokableRun(ctx, args)
			require.Empty(t, out)
			require.ErrorIs(t, err, ctx.Err())
		}
	}
}

func testRecoveryExecutionErrors[M adk.MessageType](t *testing.T) {
	forkBackend := &recoveryBackend{get: func(context.Context, string) (Skill, error) {
		return Skill{FrontMatter: FrontMatter{Name: "good", Context: ContextModeFork}}, nil
	}}
	missingHub := newTypedRecoveryTool(t, &TypedConfig[M]{Backend: forkBackend, RecoverInputErrors: true})
	out, err := missingHub.InvokableRun(context.Background(), `{"skill":"good"}`)
	require.Empty(t, out)
	require.ErrorContains(t, err, "AgentHub is not configured")

	contentErr := fmt.Errorf("content: %w", ErrSkillNotFound)
	it := newTypedRecoveryTool(t, &TypedConfig[M]{
		Backend: &recoveryBackend{}, RecoverInputErrors: true,
		BuildContent: func(context.Context, Skill, string) (string, error) { return "", contentErr },
	})
	out, err = it.InvokableRun(context.Background(), `{"skill":"good"}`)
	require.Empty(t, out)
	require.ErrorIs(t, err, contentErr)

	for _, agentErr := range []error{ErrSkillNotFound, context.Canceled, recoveryGraphInterrupt(t)} {
		it = newTypedRecoveryTool(t, &TypedConfig[M]{
			Backend:            forkBackend,
			RecoverInputErrors: true,
			AgentHub: &mockGenericAgentHub[M]{agent: &mockGenericAgent[M]{
				events: []*adk.TypedAgentEvent[M]{{Err: agentErr}},
			}},
		})
		out, err = it.InvokableRun(context.Background(), `{"skill":"good"}`)
		require.Empty(t, out)
		require.ErrorIs(t, err, agentErr)
	}
}

func TestSkillInputRecoveryExecutionErrors(t *testing.T) {
	t.Run("Message", testRecoveryExecutionErrors[*schema.Message])
	t.Run("AgenticMessage", testRecoveryExecutionErrors[*schema.AgenticMessage])
}

func TestSkillInputRecoveryConcurrentCalls(t *testing.T) {
	it := newTypedRecoveryTool(t, &Config{Backend: &recoveryBackend{}, RecoverInputErrors: true})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := it.InvokableRun(context.Background(), `{"skill":"missing"}`)
			if err != nil || out != "[skill error] failed to get skill: skill not found: missing. Check the skill name and arguments, then retry." {
				t.Errorf("output=%q error=%v", out, err)
			}
		}()
	}
	wg.Wait()
}
