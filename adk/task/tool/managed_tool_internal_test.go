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
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	taskcore "github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

func newInternalManagedTool(
	t *testing.T,
	implementation Tool,
	store background.LifecycleStore,
) (*background.Manager, *managedTool) {
	t.Helper()
	registry := NewRegistry()
	require.NoError(t, registry.Register(&Registration{
		Info: toolInfo("external"), Tool: implementation,
		Description: func(string) string {
			return "External operation"
		},
	}))
	config := &background.Config{
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task-fixed", nil
		},
	}
	if store != nil {
		config.Tasks = store
	}
	manager := mustNewBackgroundManager(t, context.Background(), config)
	timeoutMs := 1000
	wrapped, err := NewManagedTool(context.Background(), &ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "external",
		ForegroundTimeoutMs: &timeoutMs,
		SessionID:           func(context.Context) (string, error) { return "session", nil },
	})
	require.NoError(t, err)
	return manager, wrapped.(*managedTool)
}

func requireToolResultText(t *testing.T, result *schema.ToolResult, want string) {
	t.Helper()
	require.NotNil(t, result)
	require.Len(t, result.Parts, 1)
	require.Equal(t, schema.ToolPartTypeText, result.Parts[0].Type)
	require.Equal(t, want, result.Parts[0].Text)
}

func TestManagedToolDrainForegroundUpdates(t *testing.T) {
	managed := &managedTool{registration: &Registration{}}
	spec := background.Spec{ID: "task"}

	t.Run("reader error", func(t *testing.T) {
		wantErr := errors.New("reader failed")
		results := make(chan updateResult, 1)
		results <- updateResult{err: wantErr}

		err := managed.drainForegroundUpdates(
			context.Background(), spec, results, make(map[string][]byte),
		)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("reader terminal signals", func(t *testing.T) {
		for _, testCase := range []struct {
			name   string
			result updateResult
			close  bool
		}{
			{name: "EOF", result: updateResult{err: io.EOF}},
			{name: "closed channel", close: true},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				results := make(chan updateResult, 1)
				if testCase.close {
					close(results)
				} else {
					results <- testCase.result
				}
				require.NoError(t, managed.drainForegroundUpdates(
					context.Background(), spec, results, make(map[string][]byte),
				))
			})
		}
	})

	t.Run("terminal outcome requires reader closure", func(t *testing.T) {
		results := make(chan updateResult)
		startedAt := time.Now()
		err := managed.drainForegroundUpdates(
			context.Background(), spec, results, make(map[string][]byte),
		)
		require.EqualError(t, err, "task/tool: update stream did not close after terminal outcome")
		require.GreaterOrEqual(t, time.Since(startedAt), terminalUpdateDrainTime)
		require.Less(t, time.Since(startedAt), terminalUpdateDrainTime+time.Second)
	})
}

func TestManagedToolProcessForegroundUpdate(t *testing.T) {
	spec := background.Spec{ID: "task", OutputFile: "/tmp/task.out"}

	t.Run("invalid updates", func(t *testing.T) {
		managed := &managedTool{registration: &Registration{}}
		first, err := managed.processForegroundUpdate(
			context.Background(), spec, nil, make(map[string][]byte),
		)
		require.False(t, first)
		require.EqualError(t, err, "task/tool: update must not be nil")

		first, err = managed.processForegroundUpdate(
			context.Background(),
			spec,
			&Update{Data: []byte(strings.Repeat("x", maxUpdateDataBytes+1))},
			make(map[string][]byte),
		)
		require.False(t, first)
		require.EqualError(t, err, "task/tool: update data exceeds configured bounds")

		managed.recoverable = true
		first, err = managed.processForegroundUpdate(
			context.Background(), spec, &Update{Kind: "stdout"}, make(map[string][]byte),
		)
		require.False(t, first)
		require.EqualError(t, err, "task/tool: recoverable update event id is required")
	})

	t.Run("replay and conflict", func(t *testing.T) {
		managed := &managedTool{registration: &Registration{}}
		seen := make(map[string][]byte)
		first, err := managed.processForegroundUpdate(
			context.Background(),
			spec,
			&Update{EventID: "event", Data: []byte("first")},
			seen,
		)
		require.NoError(t, err)
		require.True(t, first)

		first, err = managed.processForegroundUpdate(
			context.Background(),
			spec,
			&Update{EventID: "event", Data: []byte("first")},
			seen,
		)
		require.NoError(t, err)
		require.False(t, first)

		first, err = managed.processForegroundUpdate(
			context.Background(),
			spec,
			&Update{EventID: "event", Data: []byte("different")},
			seen,
		)
		require.False(t, first)
		require.ErrorIs(t, err, background.ErrTaskEventPartConflict)
	})

	t.Run("materializer error", func(t *testing.T) {
		wantErr := errors.New("materializer unavailable")
		materializer := &materializerStub{err: wantErr}
		managed := &managedTool{registration: &Registration{Materializer: materializer}}
		first, err := managed.processForegroundUpdate(
			context.Background(),
			spec,
			&Update{EventID: "event", Kind: "stdout", Data: []byte("line")},
			make(map[string][]byte),
		)
		require.False(t, first)
		require.ErrorIs(t, err, wantErr)
		require.Len(t, materializer.requests, 1)
		require.Equal(t, "task", materializer.requests[0].TaskID)
		require.Equal(t, "event", materializer.requests[0].EventID)
		require.Equal(t, "/tmp/task.out", materializer.requests[0].Path)
		require.Equal(t, "line", string(materializer.requests[0].Data))
	})
}

func TestManagedToolWaitForeground(t *testing.T) {
	t.Run("valid update is processed before outcome", func(t *testing.T) {
		materializer := &materializerStub{}
		managed := &managedTool{
			registration: &Registration{Materializer: materializer},
		}
		managed.policy.TimeoutMs = 1000
		updates := schema.StreamReaderFromArray([]*Update{{
			EventID: "event", Kind: "stdout", Data: []byte("line"),
		}})
		start := &foregroundStart{
			spec: background.Spec{
				ID: "task", OutputFile: "/tmp/task.out",
			},
			run: &updatingRun{
				updates: updates,
				fakeRun: &fakeRun{wait: func(context.Context) (*Outcome, error) {
					return &Outcome{
						Status: taskcore.OutcomeCompleted,
						Data:   []byte(`{"ok":true}`),
					}, nil
				}},
			},
		}

		outcome, snapshot, err := managed.waitForeground(
			context.Background(), `{"value":"x"}`, start,
		)
		require.NoError(t, err)
		require.Nil(t, snapshot)
		require.Equal(t, taskcore.OutcomeCompleted, outcome.Status)
		require.JSONEq(t, `{"ok":true}`, string(outcome.Data))
		require.Len(t, materializer.requests, 1)
		require.Equal(t, "event", materializer.requests[0].EventID)
		require.Equal(t, "line", string(materializer.requests[0].Data))
	})

	t.Run("invalid update becomes a failed outcome", func(t *testing.T) {
		managed := &managedTool{
			registration: &Registration{},
		}
		managed.policy.TimeoutMs = 1000
		updates := schema.StreamReaderFromArray([]*Update{nil})
		start := &foregroundStart{
			spec: background.Spec{ID: "task"},
			run: &updatingRun{
				updates: updates,
				fakeRun: &fakeRun{wait: func(ctx context.Context) (*Outcome, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				}},
			},
		}

		outcome, snapshot, err := managed.waitForeground(
			context.Background(), `{"value":"x"}`, start,
		)
		require.NoError(t, err)
		require.Nil(t, snapshot)
		require.Equal(t, taskcore.OutcomeFailed, outcome.Status)
		require.Equal(t, "task/tool: update must not be nil", outcome.Error)
	})

	t.Run("wait error becomes a failed outcome", func(t *testing.T) {
		wantErr := errors.New("wait failed")
		managed := &managedTool{
			registration: &Registration{},
		}
		managed.policy.TimeoutMs = 1000
		start := &foregroundStart{
			spec: background.Spec{ID: "task"},
			run: &fakeRun{wait: func(context.Context) (*Outcome, error) {
				return nil, wantErr
			}},
		}

		outcome, snapshot, err := managed.waitForeground(
			context.Background(), `{"value":"x"}`, start,
		)
		require.NoError(t, err)
		require.Nil(t, snapshot)
		require.Equal(t, taskcore.OutcomeFailed, outcome.Status)
		require.Equal(t, wantErr.Error(), outcome.Error)
	})
}

func TestManagedToolStreamForeground(t *testing.T) {
	t.Run("start error is a foreground failure and finalizes mailbox", func(t *testing.T) {
		startErr := errors.New("start failed")
		manager, managed := newInternalManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return nil, startErr
			},
		}, nil)

		stream, err := managed.StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results := readAllStreamResults(t, stream)
		require.Len(t, results, 1)
		requireToolResultText(
			t,
			results[0],
			"{\"type\":\"foreground_result\",\"status\":\"failed\",\"error\":\"start failed\"}\n",
		)
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("update precedes completed result", func(t *testing.T) {
		manager, managed := newInternalManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &updatingRun{
					updates: schema.StreamReaderFromArray([]*Update{{
						Kind: "stdout", Data: []byte("line"),
					}}),
					fakeRun: &fakeRun{wait: func(context.Context) (*Outcome, error) {
						return &Outcome{
							Status: taskcore.OutcomeCompleted,
							Data:   []byte(`{"ok":true}`),
						}, nil
					}},
				}, nil
			},
		}, nil)

		stream, err := managed.StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results := readAllStreamResults(t, stream)
		require.Len(t, results, 2)
		requireToolResultText(
			t,
			results[0],
			"{\"type\":\"update\",\"update\":{\"kind\":\"stdout\",\"data\":\"bGluZQ==\"}}\n",
		)
		requireToolResultText(
			t,
			results[1],
			"{\"type\":\"foreground_result\",\"status\":\"completed\",\"description\":\"External operation\",\"output\":{\"ok\":true}}\n",
		)
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("invalid update is returned as a reader error", func(t *testing.T) {
		manager, managed := newInternalManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &updatingRun{
					updates: schema.StreamReaderFromArray([]*Update{nil}),
					fakeRun: &fakeRun{wait: func(ctx context.Context) (*Outcome, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					}},
				}, nil
			},
		}, nil)

		stream, err := managed.StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results, recvErr := readStreamResults(t, stream)
		require.Empty(t, results)
		require.EqualError(t, recvErr, "task/tool: update must not be nil")
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("mailbox finalization error accompanies the final result", func(t *testing.T) {
		finalizeErr := errors.New("seal failed")
		_, managed := newInternalManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{wait: func(context.Context) (*Outcome, error) {
					return &Outcome{Status: taskcore.OutcomeCompleted}, nil
				}}, nil
			},
		}, &mailboxFinalizationErrorStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			sealErr:       finalizeErr,
		})

		stream, err := managed.StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results, recvErr := readStreamResults(t, stream)
		require.Len(t, results, 1)
		require.ErrorIs(t, recvErr, finalizeErr)
		requireToolResultText(
			t,
			results[0],
			"{\"type\":\"foreground_result\",\"status\":\"completed\",\"description\":\"External operation\"}\n",
		)
	})

	t.Run("nil update reader is a foreground failure", func(t *testing.T) {
		var stopCalls int32
		manager, managed := newInternalManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &updatingRun{
					updates: nil,
					fakeRun: &fakeRun{
						wait: func(context.Context) (*Outcome, error) {
							t.Fatal("Wait must not run with a nil update reader")
							return nil, nil
						},
						stop: func(context.Context) error {
							atomic.AddInt32(&stopCalls, 1)
							return nil
						},
					},
				}, nil
			},
		}, nil)

		stream, err := managed.StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results := readAllStreamResults(t, stream)
		require.Len(t, results, 1)
		requireToolResultText(
			t,
			results[0],
			"{\"type\":\"foreground_result\",\"status\":\"failed\",\"description\":\"External operation\",\"error\":\"task/tool: update source returned a nil reader\"}\n",
		)
		require.Equal(t, int32(1), atomic.LoadInt32(&stopCalls))
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("foreground timeout stops the run and returns a failure", func(t *testing.T) {
		var stopCalls int32
		manager, managed := newInternalManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(ctx context.Context) (*Outcome, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					},
					stop: func(context.Context) error {
						atomic.AddInt32(&stopCalls, 1)
						return nil
					},
				}, nil
			},
		}, nil)
		managed.policy.TimeoutMs = 10

		stream, err := managed.StreamableRun(
			context.Background(), toolArgument(`{"value":"x"}`),
		)
		require.NoError(t, err)
		results := readAllStreamResults(t, stream)
		require.Len(t, results, 1)
		requireToolResultText(
			t,
			results[0],
			"{\"type\":\"foreground_result\",\"status\":\"failed\",\"description\":\"External operation\",\"error\":\"timed out after 10ms\"}\n",
		)
		require.Equal(t, int32(1), atomic.LoadInt32(&stopCalls))
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("caller cancellation stops the run and returns the context error", func(t *testing.T) {
		var stopCalls int32
		waiting := make(chan struct{})
		releaseWait := make(chan struct{})
		manager, managed := newInternalManagedTool(t, &plainFakeTool{
			start: func(context.Context, *StartRequest) (Run, error) {
				return &fakeRun{
					wait: func(ctx context.Context) (*Outcome, error) {
						close(waiting)
						<-ctx.Done()
						<-releaseWait
						return nil, ctx.Err()
					},
					stop: func(context.Context) error {
						atomic.AddInt32(&stopCalls, 1)
						close(releaseWait)
						return nil
					},
				}, nil
			},
		}, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		stream, err := managed.StreamableRun(ctx, toolArgument(`{"value":"x"}`))
		require.NoError(t, err)
		select {
		case <-waiting:
		case <-time.After(time.Second):
			t.Fatal("run did not begin waiting within 1 second")
		}
		cancel()
		results, recvErr := readStreamResults(t, stream)
		require.Empty(t, results)
		require.ErrorIs(t, recvErr, context.Canceled)
		require.Equal(t, int32(1), atomic.LoadInt32(&stopCalls))
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})
}

func TestManagedToolResumeForeground(t *testing.T) {
	newResumeContext := func(t *testing.T, state foregroundToolInterruptState) context.Context {
		t.Helper()
		ctx := core.AppendAddressSegment(
			context.Background(), compose.AddressSegmentTool, "external", "resume-call",
		)
		interruptErr := componenttool.StatefulInterrupt(ctx, nil, state)
		var signal *core.InterruptSignal
		require.ErrorAs(t, interruptErr, &signal)
		idToAddress, idToState := core.SignalToPersistenceMaps(signal)
		ctx = compose.ResumeWithData(
			context.Background(), signal.ID, json.RawMessage(`"continue"`),
		)
		ctx = core.AppendAddressSegment(
			ctx, compose.AddressSegmentTool, "external", "resume-call",
		)
		return core.PopulateInterruptState(ctx, idToAddress, idToState)
	}
	registerMailbox := func(t *testing.T, manager *background.Manager) int64 {
		t.Helper()
		registered, err := manager.RegisterMailbox(
			context.Background(),
			&taskcore.RegisterMailboxRequest{
				CandidateTaskID: "task-fixed",
				InvocationID:    "resume-invocation",
				Identity:        []byte("resume-identity"),
				RootSessionID:   "session",
			},
		)
		require.NoError(t, err)
		return registered.Mailbox.Generation
	}

	t.Run("missing checkpoint and durable task still resumes", func(t *testing.T) {
		var received *ResumeRequest
		implementation := &resumableFakeTool{
			fakeTool: &fakeTool{
				start: func(context.Context, *StartRequest) (Run, error) {
					t.Fatal("resume must not restart the tool")
					return nil, nil
				},
				recover: func(context.Context, *RecoverRequest) (Run, error) {
					t.Fatal("direct foreground resume must not recover a durable task")
					return nil, nil
				},
			},
			resume: func(_ context.Context, request *ResumeRequest) (Run, error) {
				copy := *request
				copy.Data = append([]byte(nil), request.Data...)
				copy.Checkpoint = append([]byte(nil), request.Checkpoint...)
				received = &copy
				return &fakeRun{wait: func(context.Context) (*Outcome, error) {
					return &Outcome{
						Status: taskcore.OutcomeCompleted,
						Data:   []byte(`{"resumed":true}`),
					}, nil
				}}, nil
			},
		}
		manager, managed := newInternalManagedTool(t, implementation, nil)
		state := foregroundToolInterruptState{
			TaskID: "task-fixed", ToolName: "external",
			Arguments: `{"value":"x"}`, RequestID: "approval",
			MailboxGeneration: registerMailbox(t, manager),
		}

		result, err := managed.resumeForeground(newResumeContext(t, state), state)
		require.NoError(t, err)
		requireToolResultText(
			t,
			result,
			"{\"type\":\"foreground_result\",\"status\":\"completed\",\"description\":\"External operation\",\"output\":{\"resumed\":true}}\n",
		)
		require.NotNil(t, received)
		require.Equal(t, "task-fixed", received.TaskID)
		require.Equal(t, "approval", received.RequestID)
		require.Equal(t, `"continue"`, string(received.Data))
		require.Empty(t, received.Checkpoint)
		_, err = manager.Get(context.Background(), "task-fixed")
		require.ErrorIs(t, err, background.ErrNotFound)
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("invalid resumed outcome is rejected and finalizes mailbox", func(t *testing.T) {
		implementation := &resumableFakeTool{
			fakeTool: &fakeTool{
				start:   func(context.Context, *StartRequest) (Run, error) { return nil, nil },
				recover: func(context.Context, *RecoverRequest) (Run, error) { return nil, nil },
			},
			resume: func(context.Context, *ResumeRequest) (Run, error) {
				return &fakeRun{wait: func(context.Context) (*Outcome, error) {
					return &Outcome{
						Status: taskcore.OutcomeCompleted,
						Error:  "invalid terminal error",
					}, nil
				}}, nil
			},
		}
		manager, managed := newInternalManagedTool(t, implementation, nil)
		state := foregroundToolInterruptState{
			TaskID: "task-fixed", ToolName: "external",
			Arguments: `{"value":"x"}`, RequestID: "approval",
			MailboxGeneration: registerMailbox(t, manager),
		}

		result, err := managed.resumeForeground(newResumeContext(t, state), state)
		require.Nil(t, result)
		require.EqualError(
			t,
			err,
			"task/tool: completed outcome cannot contain an error, input request, or checkpoint",
		)
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})

	t.Run("nil resumed run becomes a foreground failure", func(t *testing.T) {
		implementation := &resumableFakeTool{
			fakeTool: &fakeTool{
				start:   func(context.Context, *StartRequest) (Run, error) { return nil, nil },
				recover: func(context.Context, *RecoverRequest) (Run, error) { return nil, nil },
			},
			resume: func(context.Context, *ResumeRequest) (Run, error) {
				return nil, nil
			},
		}
		manager, managed := newInternalManagedTool(t, implementation, nil)
		state := foregroundToolInterruptState{
			TaskID: "task-fixed", ToolName: "external",
			Arguments: `{"value":"x"}`, RequestID: "approval",
			MailboxGeneration: registerMailbox(t, manager),
		}

		result, err := managed.resumeForeground(newResumeContext(t, state), state)
		require.NoError(t, err)
		requireToolResultText(
			t,
			result,
			"{\"type\":\"foreground_result\",\"status\":\"failed\",\"error\":\"task/tool: resume returned a nil run\"}\n",
		)
		waitForegroundMailboxState(t, manager, "task-fixed", taskcore.MailboxSealed)
	})
}

func TestManagedToolSpecForTaskID(t *testing.T) {
	managed := &managedTool{
		registration: &Registration{
			Info: toolInfo("external"),
			Description: func(string) string {
				return "External operation"
			},
		},
		recoverable: true,
	}

	t.Run("session lookup error", func(t *testing.T) {
		wantErr := errors.New("session store failed")
		managed.sessionID = func(context.Context) (string, error) {
			return "", wantErr
		}
		taskID, spec, err := managed.specForTaskID(
			context.Background(), "task", `{"value":"x"}`, "/tmp/task.out",
		)
		require.Empty(t, taskID)
		require.Equal(t, background.Spec{}, spec)
		require.ErrorIs(t, err, wantErr)
	})

	t.Run("session not found is a valid unscoped spec", func(t *testing.T) {
		managed.sessionID = func(context.Context) (string, error) {
			return "", nil
		}
		taskID, spec, err := managed.specForTaskID(
			context.Background(), "task", `{"value":"x"}`, "/tmp/task.out",
		)
		require.NoError(t, err)
		require.Equal(t, "task", taskID)
		require.Equal(t, "task", spec.ID)
		require.Equal(t, RecoverableExecutorKey, spec.ExecutorKey)
		require.Equal(t, "background_tool", spec.Kind)
		require.Equal(t, "External operation", spec.Description)
		require.Equal(t, "/tmp/task.out", spec.OutputFile)
		require.Empty(t, spec.RootSessionID)
		require.False(t, spec.NotifySession)
		var payload taskPayload
		require.NoError(t, json.Unmarshal(spec.Payload, &payload))
		require.Equal(t, payloadVersion, payload.Version)
		require.Equal(t, "external", payload.ToolName)
		require.JSONEq(t, `{"value":"x"}`, payload.Arguments)
	})

	t.Run("parent execution supplies task and root session", func(t *testing.T) {
		managed.sessionID = func(context.Context) (string, error) {
			return "caller-session", nil
		}
		ctx := taskcore.WithExecutionContext(context.Background(), taskcore.ExecutionContext{
			TaskID: "parent-task", RootSessionID: "root-session",
		})
		taskID, spec, err := managed.specForTaskID(
			ctx, "task", `{"value":"x"}`, "/tmp/task.out",
		)
		require.NoError(t, err)
		require.Equal(t, "task", taskID)
		require.Equal(t, "parent-task", spec.ParentTaskID)
		require.Equal(t, "root-session", spec.RootSessionID)
		require.True(t, spec.NotifySession)
	})
}

func TestManagedToolNewSpec(t *testing.T) {
	newManaged := func(
		manager *background.Manager,
		materializer OutputMaterializer,
		sessionID func(context.Context) (string, error),
	) *managedTool {
		return &managedTool{
			manager: manager,
			registration: &Registration{
				Info:         toolInfo("external"),
				Description:  func(string) string { return "External operation" },
				Materializer: materializer,
			},
			recoverable: true,
			sessionID:   sessionID,
		}
	}

	t.Run("session lookup failure precedes allocation", func(t *testing.T) {
		wantErr := errors.New("session lookup failed")
		allocations := 0
		manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
			IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
				allocations++
				return "unexpected", nil
			},
		})
		managed := newManaged(
			manager,
			nil,
			func(context.Context) (string, error) { return "", wantErr },
		)

		taskID, spec, err := managed.newSpec(context.Background(), `{}`)
		require.Empty(t, taskID)
		require.Equal(t, background.Spec{}, spec)
		require.ErrorIs(t, err, wantErr)
		require.Zero(t, allocations)
	})

	t.Run("task ID allocation failure is propagated", func(t *testing.T) {
		wantErr := errors.New("allocation failed")
		manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
			IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
				return "", wantErr
			},
		})
		managed := newManaged(
			manager,
			nil,
			func(context.Context) (string, error) { return "session", nil },
		)

		taskID, spec, err := managed.newSpec(context.Background(), `{}`)
		require.Empty(t, taskID)
		require.Equal(t, background.Spec{}, spec)
		require.ErrorIs(t, err, wantErr)
	})

	for _, testCase := range []struct {
		name         string
		materializer OutputMaterializer
		wantErr      string
	}{
		{
			name: "output reservation failure",
			materializer: reserveFailure{
				err: errors.New("storage unavailable"),
			},
			wantErr: "task/tool: reserve output: storage unavailable",
		},
		{
			name:         "empty output reservation",
			materializer: reserveFailure{},
			wantErr:      "task/tool: output materializer returned an empty path",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
				IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
					return "task-fixed", nil
				},
			})
			managed := newManaged(
				manager,
				testCase.materializer,
				func(context.Context) (string, error) { return "session", nil },
			)

			taskID, spec, err := managed.newSpec(context.Background(), `{}`)
			require.Empty(t, taskID)
			require.Equal(t, background.Spec{}, spec)
			require.EqualError(t, err, testCase.wantErr)
		})
	}

	t.Run("builds a nested recoverable spec", func(t *testing.T) {
		var allocation *background.AllocateTaskIDRequest
		manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
			IDGen: func(
				_ context.Context,
				request *background.AllocateTaskIDRequest,
			) (string, error) {
				copy := *request
				allocation = &copy
				return "task-child", nil
			},
		})
		materializer := &countingMaterializer{}
		managed := newManaged(
			manager,
			materializer,
			func(context.Context) (string, error) { return "caller-session", nil },
		)
		ctx := taskcore.WithExecutionContext(
			context.Background(),
			taskcore.ExecutionContext{
				TaskID: "task-parent", RootSessionID: "root-session",
			},
		)

		taskID, spec, err := managed.newSpec(ctx, `{"value":"x"}`)
		require.NoError(t, err)
		require.Equal(t, "task-child", taskID)
		require.Equal(t, &background.AllocateTaskIDRequest{Kind: "background_tool"}, allocation)
		require.Equal(t, []string{"task-child"}, materializer.reserved)
		require.Equal(t, "task-child", spec.ID)
		require.Equal(t, RecoverableExecutorKey, spec.ExecutorKey)
		require.Equal(t, "background_tool", spec.Kind)
		require.Equal(t, "External operation", spec.Description)
		require.Equal(t, "/outputs/task-child", spec.OutputFile)
		require.Equal(t, "task-parent", spec.ParentTaskID)
		require.Equal(t, "root-session", spec.RootSessionID)
		require.True(t, spec.NotifySession)
		var payload taskPayload
		require.NoError(t, json.Unmarshal(spec.Payload, &payload))
		require.Equal(t, taskPayload{
			Version: payloadVersion, ToolName: "external", Arguments: `{"value":"x"}`,
		}, payload)
	})
}

func TestManagedToolRenderForegroundTask(t *testing.T) {
	managed := &managedTool{registration: &Registration{}}

	t.Run("invalid snapshot", func(t *testing.T) {
		result, err := managed.renderForegroundTask(context.Background(), "", nil)
		require.Nil(t, result)
		require.EqualError(t, err, "task/tool: foreground task result is required")

		result, err = managed.renderForegroundTask(
			context.Background(),
			"",
			&background.TaskSnapshot{Status: background.StatusRunning},
		)
		require.Nil(t, result)
		require.EqualError(
			t,
			err,
			`task/tool: foreground task reached non-boundary status "running"`,
		)
	})

	for _, testCase := range []struct {
		name     string
		status   background.Status
		taskErr  string
		wantWire string
	}{
		{
			name: "failed", status: background.StatusFailed, taskErr: "operation failed",
			wantWire: "{\"type\":\"foreground_result\",\"status\":\"failed\"," +
				"\"description\":\"External operation\",\"error\":\"operation failed\"}\n",
		},
		{
			name: "canceled", status: background.StatusCanceled, taskErr: "operation canceled",
			wantWire: "{\"type\":\"foreground_result\",\"status\":\"canceled\"," +
				"\"description\":\"External operation\",\"error\":\"operation canceled\"}\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := managed.renderForegroundTask(
				context.Background(),
				"",
				&background.TaskSnapshot{
					Spec:   background.Spec{Description: "External operation"},
					Status: testCase.status, ResultError: testCase.taskErr,
				},
			)
			require.NoError(t, err)
			requireToolResultText(t, result, testCase.wantWire)
		})
	}
}

func TestManagedToolInterruptForeground(t *testing.T) {
	managed := &managedTool{
		registration: &Registration{Info: toolInfo("external")},
	}
	start := &foregroundStart{
		arguments: `{"value":"x"}`,
		spec:      background.Spec{ID: "task"},
	}

	for _, testCase := range []struct {
		name    string
		outcome *Outcome
		wantErr string
	}{
		{
			name:    "missing outcome",
			wantErr: "task/tool: foreground wait-input requires an input request",
		},
		{
			name:    "missing input request",
			outcome: &Outcome{Status: taskcore.OutcomeInterrupted},
			wantErr: "task/tool: foreground wait-input requires an input request",
		},
		{
			name: "invalid input request",
			outcome: &Outcome{
				Status: taskcore.OutcomeInterrupted,
				InputRequest: &InputRequest{
					ID: "approval", Data: json.RawMessage(`{`),
				},
			},
			wantErr: "task/tool: input request data must be valid JSON",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := managed.interruptForeground(
				context.Background(), start, testCase.outcome,
			)
			require.EqualError(t, err, testCase.wantErr)
		})
	}
}

func TestManagedToolRenderLaunchResult(t *testing.T) {
	managed := &managedTool{registration: &Registration{}}
	waitingCheckpoint, err := encodeManagedCheckpoint(
		&InputRequest{
			ID: "approval", Data: json.RawMessage(`{"question":"approve?"}`),
		},
		nil,
	)
	require.NoError(t, err)

	for _, testCase := range []struct {
		name     string
		snapshot *background.TaskSnapshot
		wantWire string
	}{
		{
			name: "completed",
			snapshot: &background.TaskSnapshot{
				Spec:   background.Spec{ID: "task-completed", Description: "completed"},
				Status: background.StatusCompleted, ResultData: []byte(`{"answer":42}`),
			},
			wantWire: "{\"type\":\"launch_result\",\"task_id\":\"task-completed\"," +
				"\"status\":\"completed\",\"description\":\"completed\",\"output\":{\"answer\":42}}\n",
		},
		{
			name: "failed",
			snapshot: &background.TaskSnapshot{
				Spec:   background.Spec{ID: "task-failed", Description: "failed"},
				Status: background.StatusFailed, ResultError: "operation failed",
			},
			wantWire: "{\"type\":\"launch_result\",\"task_id\":\"task-failed\"," +
				"\"status\":\"failed\",\"description\":\"failed\",\"error\":\"operation failed\"}\n",
		},
		{
			name: "canceled",
			snapshot: &background.TaskSnapshot{
				Spec:   background.Spec{ID: "task-canceled", Description: "canceled"},
				Status: background.StatusCanceled, ResultError: "operation canceled",
			},
			wantWire: "{\"type\":\"launch_result\",\"task_id\":\"task-canceled\"," +
				"\"status\":\"canceled\",\"description\":\"canceled\",\"error\":\"operation canceled\"}\n",
		},
		{
			name: "waiting input",
			snapshot: &background.TaskSnapshot{
				Spec: background.Spec{
					ID: "task-waiting", Description: "waiting",
					ExecutorKey: RecoverableExecutorKey, Kind: "background_tool",
				},
				Status: background.StatusWaitingInput, Checkpoint: waitingCheckpoint,
			},
			wantWire: "{\"type\":\"launch_result\",\"task_id\":\"task-waiting\"," +
				"\"status\":\"waiting_input\",\"description\":\"waiting\"," +
				"\"input_request\":{\"id\":\"approval\",\"data\":{\"question\":\"approve?\"}}}\n",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, renderErr := managed.renderLaunchResult(
				context.Background(), testCase.snapshot,
			)
			require.NoError(t, renderErr)
			requireToolResultText(t, result, testCase.wantWire)
		})
	}

	result, err := managed.renderLaunchResult(
		context.Background(), &background.TaskSnapshot{},
	)
	require.Nil(t, result)
	require.EqualError(t, err, "task/tool: launch result requires a task id")

	t.Run("completed rich result follows the control envelope", func(t *testing.T) {
		managed.registration.RenderResult = func(
			context.Context,
			*background.TaskSnapshot,
		) (*schema.ToolResult, error) {
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{
				Type: schema.ToolPartTypeText, Text: "rich output",
			}}}, nil
		}
		result, renderErr := managed.renderLaunchResult(
			context.Background(),
			&background.TaskSnapshot{
				Spec:   background.Spec{ID: "task-rich", Description: "rich"},
				Status: background.StatusCompleted,
			},
		)
		require.NoError(t, renderErr)
		require.Len(t, result.Parts, 2)
		require.Equal(
			t,
			"{\"type\":\"launch_result\",\"task_id\":\"task-rich\","+
				"\"status\":\"completed\",\"description\":\"rich\"}\n",
			result.Parts[0].Text,
		)
		require.Equal(t, schema.ToolPartTypeText, result.Parts[1].Type)
		require.Equal(t, "rich output", result.Parts[1].Text)
	})
}

func TestLifecycleStatusForOutcome(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		outcome    taskcore.OutcomeStatus
		wantStatus background.Status
		wantErr    string
	}{
		{
			name: "completed", outcome: taskcore.OutcomeCompleted,
			wantStatus: background.StatusCompleted,
		},
		{
			name: "interrupted", outcome: taskcore.OutcomeInterrupted,
			wantStatus: background.StatusWaitingInput,
		},
		{
			name: "failed", outcome: taskcore.OutcomeFailed,
			wantStatus: background.StatusFailed,
		},
		{
			name: "canceled", outcome: taskcore.OutcomeCanceled,
			wantStatus: background.StatusCanceled,
		},
		{
			name:    "unknown",
			outcome: taskcore.OutcomeStatus(99),
			wantErr: "task/tool: unsupported outcome status 99",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			status, err := lifecycleStatusForOutcome(testCase.outcome)
			if testCase.wantErr != "" {
				require.Empty(t, status)
				require.EqualError(t, err, testCase.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.wantStatus, status)
		})
	}
}
