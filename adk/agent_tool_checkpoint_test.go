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

package adk

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type parallelAgentToolCheckpointModel struct{}

type nestedResumeInfoWrapper struct {
	Info *compose.InterruptInfo
}

func init() {
	schema.RegisterName[*nestedResumeInfoWrapper](
		"_eino_adk_test_nested_resume_info_wrapper")
}

func (m *parallelAgentToolCheckpointModel) Generate(_ context.Context, input []*schema.Message,
	_ ...model.Option) (*schema.Message, error) {
	return m.response(input), nil
}

func (m *parallelAgentToolCheckpointModel) Stream(_ context.Context, input []*schema.Message,
	_ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.response(input)}), nil
}

func (m *parallelAgentToolCheckpointModel) WithTools(_ []*schema.ToolInfo) (
	model.ToolCallingChatModel, error,
) {
	return m, nil
}

func (m *parallelAgentToolCheckpointModel) response(input []*schema.Message) *schema.Message {
	var results []string
	for _, message := range input {
		if message.Role == schema.Tool {
			results = append(results, message.ToolCallID+"="+message.Content)
		}
	}
	if len(results) > 0 {
		sort.Strings(results)
		return schema.AssistantMessage(strings.Join(results, ";"), nil)
	}
	return schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "parent-call-a",
			Function: schema.FunctionCall{
				Name:      "SameChild",
				Arguments: `{"request":"state-a"}`,
			},
		},
		{
			ID: "parent-call-b",
			Function: schema.FunctionCall{
				Name:      "SameChild",
				Arguments: `{"request":"state-b"}`,
			},
		},
	})
}

type parallelAgentToolCheckpointAgent struct {
	observedMu    sync.Mutex
	observed      []string
	resumeBarrier *timeoutChannelBarrier
	resumeCalls   int32
}

func (a *parallelAgentToolCheckpointAgent) Name(ctx context.Context) string {
	if a.resumeBarrier != nil && len(core.GetCurrentAddress(ctx)) == 0 &&
		atomic.AddInt32(&a.resumeCalls, 1) <= 2 {
		a.resumeBarrier.wait()
	}
	return "SameChild"
}

func (*parallelAgentToolCheckpointAgent) Description(context.Context) string {
	return "parallel checkpoint child"
}

func (*parallelAgentToolCheckpointAgent) Run(ctx context.Context, input *AgentInput,
	_ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer generator.Close()
		if input == nil || len(input.Messages) == 0 {
			generator.Send(&AgentEvent{Err: fmt.Errorf("parallel AgentTool child has no input")})
			return
		}
		state := input.Messages[len(input.Messages)-1].Content
		generator.Send(StatefulInterrupt(ctx, state, state))
	}()
	return iterator
}

func (a *parallelAgentToolCheckpointAgent) Resume(ctx context.Context, info *ResumeInfo,
	_ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer generator.Close()
		state, hasState := info.InterruptState.(string)
		data, hasData := info.ResumeData.(string)
		a.observedMu.Lock()
		a.observed = append(a.observed, fmt.Sprintf("%s target=%t data=%t:%s",
			state, info.IsResumeTarget, hasData, data))
		a.observedMu.Unlock()
		if !info.WasInterrupted || !hasState {
			generator.Send(&AgentEvent{Err: fmt.Errorf("parallel AgentTool child lost interrupt state")})
			return
		}
		if !info.IsResumeTarget {
			generator.Send(StatefulInterrupt(ctx, state, state))
			return
		}
		if !hasData {
			generator.Send(&AgentEvent{Err: fmt.Errorf("parallel AgentTool child lost resume payload")})
			return
		}
		generator.Send(EventFromMessage(schema.AssistantMessage(state+"|"+data, nil),
			nil, schema.Assistant, ""))
	}()
	return iterator
}

func (a *parallelAgentToolCheckpointAgent) armResumeBarrier() {
	a.resumeBarrier = newTimeoutChannelBarrier(2, 5*time.Second)
	atomic.StoreInt32(&a.resumeCalls, 0)
}

func (a *parallelAgentToolCheckpointAgent) observations() []string {
	a.observedMu.Lock()
	defer a.observedMu.Unlock()
	return append([]string(nil), a.observed...)
}

type timeoutChannelBarrier struct {
	arrivals chan struct{}
	release  chan struct{}
	done     chan struct{}
	err      error
}

func newTimeoutChannelBarrier(participants int, timeout time.Duration) *timeoutChannelBarrier {
	return newTimeoutChannelBarrierWithFirstArrivalSync(participants, timeout, nil, nil)
}

func newTimeoutChannelBarrierWithFirstArrivalSync(participants int, timeout time.Duration,
	firstArrival chan<- struct{}, startTimeout <-chan struct{}) *timeoutChannelBarrier {
	barrier := &timeoutChannelBarrier{
		arrivals: make(chan struct{}, participants),
		release:  make(chan struct{}),
		done:     make(chan struct{}),
	}
	go func() {
		var timer *time.Timer
		var timeoutC <-chan time.Time
		if firstArrival == nil {
			timer = time.NewTimer(timeout)
			timeoutC = timer.C
		}
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for arrived := 0; arrived < participants; arrived++ {
			select {
			case <-barrier.arrivals:
				if arrived == 0 && firstArrival != nil {
					firstArrival <- struct{}{}
					<-startTimeout
					timer = time.NewTimer(timeout)
					timeoutC = timer.C
				}
			case <-timeoutC:
				barrier.err = fmt.Errorf(
					"parallel AgentTool barrier timed out after %d of %d arrivals",
					arrived, participants)
				close(barrier.release)
				close(barrier.done)
				return
			}
		}
		close(barrier.release)
		close(barrier.done)
	}()
	return barrier
}

func (b *timeoutChannelBarrier) wait() {
	select {
	case b.arrivals <- struct{}{}:
	case <-b.release:
		return
	}
	<-b.release
}

func (b *timeoutChannelBarrier) result() error {
	<-b.done
	return b.err
}

func TestTimeoutChannelBarrierReleasesWaitersOnTimeout(t *testing.T) {
	firstArrival := make(chan struct{})
	startTimeout := make(chan struct{})
	barrier := newTimeoutChannelBarrierWithFirstArrivalSync(2, 100*time.Millisecond,
		firstArrival, startTimeout)
	firstDone := make(chan struct{})
	go func() {
		barrier.wait()
		close(firstDone)
	}()

	select {
	case <-firstArrival:
	case <-time.After(time.Second):
		t.Fatal("first barrier waiter did not arrive")
	}
	select {
	case <-firstDone:
		t.Fatal("arrived barrier waiter was released before timeout")
	default:
	}
	close(startTimeout)
	require.EqualError(t, barrier.result(),
		"parallel AgentTool barrier timed out after 1 of 2 arrivals")
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("arrived barrier waiter remained blocked after timeout")
	}

	lateDone := make(chan struct{})
	go func() {
		barrier.wait()
		close(lateDone)
	}()
	select {
	case <-lateDone:
	case <-time.After(time.Second):
		t.Fatal("late barrier waiter remained blocked after timeout")
	}
}

func TestAgentToolInterruptStateCompatibility(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		got, relativeAddress, err := decodeAgentToolInterruptState([]byte("legacy"), "agent")
		require.NoError(t, err)
		require.Equal(t, []byte("legacy"), got)
		require.False(t, relativeAddress)
	})
	t.Run("v1_absolute", func(t *testing.T) {
		got, relativeAddress, err := decodeAgentToolInterruptState(&agentToolInterruptStateV1{
			Version:          agentToolInterruptStateVersionV1,
			BridgeCheckpoint: []byte("v1"),
		}, "agent")
		require.NoError(t, err)
		require.Equal(t, []byte("v1"), got)
		require.False(t, relativeAddress)
	})
	t.Run("v2_relative", func(t *testing.T) {
		got, relativeAddress, err := decodeAgentToolInterruptState(&agentToolInterruptStateV2{
			Version:          agentToolInterruptStateVersionV2,
			BridgeCheckpoint: []byte("v2"),
		}, "agent")
		require.NoError(t, err)
		require.Equal(t, []byte("v2"), got)
		require.True(t, relativeAddress)
	})
	t.Run("unsupported_version", func(t *testing.T) {
		_, _, err := decodeAgentToolInterruptState(&agentToolInterruptStateV1{
			Version:          agentToolInterruptStateVersionV1 + 1,
			BridgeCheckpoint: []byte("v1"),
		}, "agent")
		require.EqualError(t, err, "agent tool 'agent' has unsupported interrupt state version")
	})
	t.Run("unsupported_v2_forward_version", func(t *testing.T) {
		_, _, err := decodeAgentToolInterruptState(&agentToolInterruptStateV2{
			Version:          agentToolInterruptStateVersionV2 + 1,
			BridgeCheckpoint: []byte("v2"),
		}, "agent")
		require.EqualError(t, err, "agent tool 'agent' has unsupported interrupt state version")
	})
	t.Run("empty_checkpoint", func(t *testing.T) {
		_, _, err := decodeAgentToolInterruptState(&agentToolInterruptStateV1{
			Version: agentToolInterruptStateVersionV1,
		}, "agent")
		require.EqualError(t, err, "agent tool 'agent' interrupt state has empty bridge checkpoint")
	})
	t.Run("invalid_type", func(t *testing.T) {
		_, _, err := decodeAgentToolInterruptState("invalid", "agent")
		require.EqualError(t, err, "agent tool 'agent' has invalid interrupt state type string")
	})
	t.Run("legacy_reader_fails_loudly", func(t *testing.T) {
		assertCheckpointCompatLegacyReaderRejectsValue(t, buildCheckpointCompatLegacyReader(t),
			&agentToolInterruptStateV2{
				Version:          agentToolInterruptStateVersionV2,
				BridgeCheckpoint: []byte("checkpoint"),
			},
			"_eino_adk_agent_tool_interrupt_state_v2")
	})
}

func TestCheckpointAgentToolStateData(t *testing.T) {
	tests := []struct {
		name  string
		state any
		want  []byte
		ok    bool
	}{
		{
			name: "v1",
			state: &agentToolInterruptStateV1{
				Version:          agentToolInterruptStateVersionV1,
				BridgeCheckpoint: []byte("v1"),
			},
			want: []byte("v1"),
			ok:   true,
		},
		{
			name: "v2",
			state: &agentToolInterruptStateV2{
				Version:          agentToolInterruptStateVersionV2,
				BridgeCheckpoint: []byte("v2"),
			},
			want: []byte("v2"),
			ok:   true,
		},
		{name: "nil_v1", state: (*agentToolInterruptStateV1)(nil)},
		{name: "nil_v2", state: (*agentToolInterruptStateV2)(nil)},
		{
			name: "wrong_v1_version",
			state: &agentToolInterruptStateV1{
				Version:          agentToolInterruptStateVersionV2,
				BridgeCheckpoint: []byte("v1"),
			},
		},
		{
			name: "wrong_v2_version",
			state: &agentToolInterruptStateV2{
				Version:          agentToolInterruptStateVersionV1,
				BridgeCheckpoint: []byte("v2"),
			},
		},
		{name: "unrelated", state: []byte("legacy")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := checkpointAgentToolStateData(tt.state)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestAgentToolV2ParallelSameNameResumeIsolation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode := "invoke"
		if streaming {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			child := &parallelAgentToolCheckpointAgent{}
			agentTool := NewAgentTool(context.Background(), child)
			parent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
				Name:        "ParallelParent",
				Description: "parallel checkpoint parent",
				Model:       &parallelAgentToolCheckpointModel{},
				ToolsConfig: ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []componenttool.BaseTool{agentTool},
					},
				},
			})
			require.NoError(t, err)

			const checkpointID = "parallel-same-name-" + "checkpoint"
			store := newCheckpointCompatStore()
			runner := NewRunner(context.Background(), RunnerConfig{
				Agent:           parent,
				EnableStreaming: streaming,
				CheckPointStore: store,
			})
			iter := runner.Query(context.Background(), "start", WithCheckPointID(checkpointID))
			var interruptContexts []*InterruptCtx
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				require.NoError(t, event.Err)
				if event.Action != nil && event.Action.Interrupted != nil {
					interruptContexts = append(interruptContexts,
						event.Action.Interrupted.InterruptContexts...)
				}
			}
			require.Len(t, interruptContexts, 2)

			raw, exists, err := store.Get(context.Background(), checkpointID)
			require.NoError(t, err)
			require.True(t, exists)
			legacyCount, v1Count, v2Count := countCheckpointCompatAgentToolStates(t, raw)
			require.Zero(t, legacyCount)
			require.Zero(t, v1Count)
			require.Equal(t, 2, v2Count)

			targets := make(map[string]any, len(interruptContexts))
			localAddresses := make(map[string]Address, len(interruptContexts))
			for _, interruptCtx := range interruptContexts {
				state, ok := interruptCtx.Info.(string)
				require.True(t, ok)
				require.Contains(t, []string{"state-a", "state-b"}, state)
				targets[interruptCtx.ID] = "resume-" + strings.TrimPrefix(state, "state-")
				localAddresses[state] = parallelAgentToolLocalAddress(
					t, interruptCtx.Address, "parent-call-"+strings.TrimPrefix(state, "state-"))
			}
			require.Equal(t, localAddresses["state-a"], localAddresses["state-b"],
				"same-name AgentTool children must expose identical child-local addresses")

			child.armResumeBarrier()
			resumed, err := runner.ResumeWithParams(context.Background(), checkpointID,
				&ResumeParams{Targets: targets})
			require.NoError(t, err)
			var final string
			for {
				event, ok := resumed.Next()
				if !ok {
					break
				}
				require.NoError(t, event.Err)
				if event.Output == nil || event.Output.MessageOutput == nil {
					continue
				}
				message, messageErr := event.Output.MessageOutput.GetMessage()
				require.NoError(t, messageErr)
				if message != nil && message.Role == schema.Assistant && message.Content != "" {
					final = message.Content
				}
			}
			require.NoError(t, child.resumeBarrier.result())
			observations := child.observations()
			sort.Strings(observations)
			require.Equal(t, []string{
				"state-a target=true data=true:resume-a",
				"state-b target=true data=true:resume-b",
			}, observations)
			require.Equal(t,
				"parent-call-a=state-a|resume-a;parent-call-b=state-b|resume-b",
				final)
		})
	}
}

type nestedResumeObservingAgentTool struct {
	inner componenttool.InvokableTool

	mu           sync.Mutex
	observations []string
}

func (t *nestedResumeObservingAgentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *nestedResumeObservingAgentTool) InvokableRun(ctx context.Context, argumentsInJSON string,
	opts ...componenttool.Option) (string, error) {
	wasInterrupted, _, _ := componenttool.GetInterruptState[any](ctx)
	if wasInterrupted {
		isTarget, hasData, data := componenttool.GetResumeContext[string](ctx)
		t.mu.Lock()
		t.observations = append(t.observations,
			fmt.Sprintf("target=%t data=%t:%s", isTarget, hasData, data))
		t.mu.Unlock()
	}
	return t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
}

func (t *nestedResumeObservingAgentTool) observed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.observations...)
}

type nestedResumeSiblingTool struct {
	mu           sync.Mutex
	observations []string
}

type nestedResumeCheckpointModel struct {
	checkpointCompatModel
	generateCalls int32
	streamCalls   int32
}

func (m *nestedResumeCheckpointModel) Generate(ctx context.Context, input []*schema.Message,
	opts ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.generateCalls, 1)
	return m.checkpointCompatModel.Generate(ctx, input, opts...)
}

func (m *nestedResumeCheckpointModel) Stream(ctx context.Context, input []*schema.Message,
	opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	atomic.AddInt32(&m.streamCalls, 1)
	return m.checkpointCompatModel.Stream(ctx, input, opts...)
}

func (m *nestedResumeCheckpointModel) WithTools(_ []*schema.ToolInfo) (
	model.ToolCallingChatModel, error) {
	return m, nil
}

func (*nestedResumeSiblingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "Sibling", Desc: "sibling resume target"}, nil
}

func (t *nestedResumeSiblingTool) InvokableRun(ctx context.Context, _ string,
	_ ...componenttool.Option) (string, error) {
	wasInterrupted, hasState, state := componenttool.GetInterruptState[string](ctx)
	if !wasInterrupted {
		return "", componenttool.StatefulInterrupt(ctx, "sibling-interrupt", "sibling-state")
	}
	isTarget, hasData, data := componenttool.GetResumeContext[string](ctx)
	t.mu.Lock()
	t.observations = append(t.observations, fmt.Sprintf(
		"state=%t:%s target=%t data=%t:%s", hasState, state, isTarget, hasData, data))
	t.mu.Unlock()
	if !hasState {
		return "", fmt.Errorf("sibling lost interrupt state")
	}
	if !isTarget || !hasData {
		return "", componenttool.StatefulInterrupt(ctx, "sibling-reinterrupt", state)
	}
	return "sibling|" + data, nil
}

func (t *nestedResumeSiblingTool) observed() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.observations...)
}

func TestAgentToolV2NestedComposeInfoResumeTargets(t *testing.T) {
	type nestedSource struct {
		name string
		set  func(*compose.InterruptInfo, *compose.InterruptInfo)
	}
	sources := []nestedSource{
		{
			name: "state",
			set: func(root, target *compose.InterruptInfo) {
				root.State = &nestedResumeInfoWrapper{Info: target}
			},
		},
		{
			name: "rerun_nodes_extra",
			set: func(root, target *compose.InterruptInfo) {
				if root.RerunNodesExtra == nil {
					root.RerunNodesExtra = make(map[string]any)
				}
				root.RerunNodesExtra["nested-target"] = &nestedResumeInfoWrapper{Info: target}
			},
		},
		{
			name: "context_info",
			set: func(root, target *compose.InterruptInfo) {
				root.InterruptContexts = append(root.InterruptContexts, &InterruptCtx{
					Info: &nestedResumeInfoWrapper{Info: target},
				})
			},
		},
	}

	for _, streaming := range []bool{false, true} {
		mode := "invoke"
		if streaming {
			mode = "stream"
		}
		for _, source := range sources {
			t.Run(mode+"/"+source.name, func(t *testing.T) {
				ctx := context.Background()
				grandchild := &parallelAgentToolCheckpointAgent{}
				grandchildTool := NewAgentTool(ctx, grandchild)
				parentModel := &nestedResumeCheckpointModel{
					checkpointCompatModel: checkpointCompatModel{toolNames: []string{"SameChild"}},
				}
				parentAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
					Name:        "NestedParent",
					Description: "nested resume parent",
					Model:       parentModel,
					ToolsConfig: ToolsConfig{
						ToolsNodeConfig: compose.ToolsNodeConfig{
							Tools: []componenttool.BaseTool{grandchildTool},
						},
					},
				})
				require.NoError(t, err)
				parentBaseTool := NewAgentTool(ctx, parentAgent)
				parentInvokableTool, ok := parentBaseTool.(componenttool.InvokableTool)
				require.True(t, ok)
				parentTool := &nestedResumeObservingAgentTool{inner: parentInvokableTool}
				siblingTool := &nestedResumeSiblingTool{}
				outerModel := &nestedResumeCheckpointModel{
					checkpointCompatModel: checkpointCompatModel{
						toolNames: []string{"NestedParent", "Sibling"},
					},
				}
				outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
					Name:        "NestedOuter",
					Description: "nested resume outer",
					Model:       outerModel,
					ToolsConfig: ToolsConfig{
						ToolsNodeConfig: compose.ToolsNodeConfig{
							Tools: []componenttool.BaseTool{parentTool, siblingTool},
						},
					},
				})
				require.NoError(t, err)

				checkpointID := "nested-compose-info-" + mode + "-" + source.name
				store := newCheckpointCompatStore()
				runner := NewRunner(ctx, RunnerConfig{
					Agent:           outerAgent,
					EnableStreaming: streaming,
					CheckPointStore: store,
				})
				iter := runner.Query(ctx, "start", WithCheckPointID(checkpointID))
				var interruptContexts []*InterruptCtx
				for {
					event, more := iter.Next()
					if !more {
						break
					}
					require.NoError(t, event.Err)
					if event.Action != nil && event.Action.Interrupted != nil {
						interruptContexts = append(interruptContexts,
							event.Action.Interrupted.InterruptContexts...)
					}
				}
				require.Len(t, interruptContexts, 2)

				grandchildID, parentID, siblingID := nestedResumeTargetIDs(
					t, interruptContexts)
				raw, exists, err := store.Get(ctx, checkpointID)
				require.NoError(t, err)
				require.True(t, exists)
				raw = moveAgentToolTargetToNestedComposeInfo(
					t, raw, grandchildID, source.set)
				require.NoError(t, store.Set(ctx, checkpointID, raw))

				const (
					grandchildPayload = "grandchild-payload"
					parentPayload     = "parent-payload"
					siblingPayload    = "sibling-payload"
				)
				resumed, err := runner.ResumeWithParams(ctx, checkpointID, &ResumeParams{
					Targets: map[string]any{
						grandchildID: grandchildPayload,
						parentID:     parentPayload,
						siblingID:    siblingPayload,
					},
				})
				require.NoError(t, err)

				var terminalOutputs, reinterrupts int
				for {
					event, more := resumed.Next()
					if !more {
						break
					}
					require.NoError(t, event.Err)
					if event.Action != nil && event.Action.Interrupted != nil {
						reinterrupts++
					}
					if event.Output == nil || event.Output.MessageOutput == nil {
						continue
					}
					message, messageErr := event.Output.MessageOutput.GetMessage()
					require.NoError(t, messageErr)
					if message != nil && message.Role == schema.Assistant &&
						message.Content == "completed" {
						terminalOutputs++
					}
				}

				require.Equal(t, []string{
					"continue target=true data=true:" + grandchildPayload,
				}, grandchild.observations())
				require.Equal(t, []string{
					"target=true data=true:" + parentPayload,
				}, parentTool.observed())
				require.Equal(t, []string{
					"state=true:sibling-state target=true data=true:" + siblingPayload,
				}, siblingTool.observed())
				require.Equal(t, 1, terminalOutputs)
				require.Zero(t, reinterrupts)
				if streaming {
					require.Zero(t, atomic.LoadInt32(&parentModel.generateCalls))
					require.Zero(t, atomic.LoadInt32(&outerModel.generateCalls))
					require.Positive(t, atomic.LoadInt32(&parentModel.streamCalls))
					require.Positive(t, atomic.LoadInt32(&outerModel.streamCalls))
				} else {
					require.Positive(t, atomic.LoadInt32(&parentModel.generateCalls))
					require.Positive(t, atomic.LoadInt32(&outerModel.generateCalls))
					require.Zero(t, atomic.LoadInt32(&parentModel.streamCalls))
					require.Zero(t, atomic.LoadInt32(&outerModel.streamCalls))
				}
			})
		}
	}
}

func nestedResumeTargetIDs(t *testing.T, contexts []*InterruptCtx) (
	grandchildID, parentID, siblingID string) {
	t.Helper()
	for _, interruptCtx := range contexts {
		if interruptCtx.Info == "sibling-interrupt" {
			siblingID = interruptCtx.ID
			continue
		}
		if interruptCtx.Info != "continue" {
			continue
		}
		grandchildID = interruptCtx.ID
		for current := interruptCtx.Parent; current != nil; current = current.Parent {
			if len(current.Address) == 0 {
				continue
			}
			segment := current.Address[len(current.Address)-1]
			if segment.Type == AddressSegmentTool && segment.ID == "NestedParent" {
				parentID = current.ID
				break
			}
		}
	}
	require.NotEmpty(t, grandchildID)
	require.NotEmpty(t, parentID)
	require.NotEmpty(t, siblingID)
	return grandchildID, parentID, siblingID
}

func moveAgentToolTargetToNestedComposeInfo(t *testing.T, raw []byte, targetID string,
	setNested func(*compose.InterruptInfo, *compose.InterruptInfo)) []byte {
	t.Helper()
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NoError(t, restoreRunnerCheckpointProjection(&outer))
	outer.ProjectionV1 = nil

	var rewritten int
	for id, state := range outer.InterruptID2State {
		composeData, ok := state.State.([]byte)
		if !ok {
			continue
		}
		transformed, err := compose.TransformCheckpointValues(composeData, &gobSerializer{},
			func(_ compose.NodePath, location compose.CheckpointValueLocation,
				value any) (any, bool, error) {
				if rewritten > 0 || location.Kind != compose.CheckpointValueInterruptState {
					return value, false, nil
				}
				agentToolState, ok := value.(*agentToolInterruptStateV2)
				if !ok || agentToolState == nil {
					return value, false, nil
				}
				child := moveRunnerTargetToNestedComposeInfo(
					t, agentToolState.BridgeCheckpoint, targetID, setNested)
				cloned := *agentToolState
				cloned.BridgeCheckpoint = child
				rewritten++
				return &cloned, true, nil
			})
		require.NoError(t, err)
		state.State = transformed
		outer.InterruptID2State[id] = state
	}
	require.Equal(t, 1, rewritten)
	if outer.InfoDataSourceInterruptID != "" {
		chatModelInfo, ok := outer.Info.Data.(*ChatModelAgentInterruptInfo)
		require.True(t, ok)
		require.NotNil(t, chatModelInfo)
		chatModelInfo.Data = nil
	}
	encoded, err := encodeRunnerCheckpoint(&outer)
	require.NoError(t, err)
	return encoded
}

func moveRunnerTargetToNestedComposeInfo(t *testing.T, raw []byte, targetID string,
	setNested func(*compose.InterruptInfo, *compose.InterruptInfo)) []byte {
	t.Helper()
	var checkpoint serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&checkpoint))
	require.NoError(t, restoreRunnerCheckpointProjection(&checkpoint))
	require.NoError(t, restoreRunnerCheckpointInfoData(&checkpoint))
	checkpoint.ProjectionV1 = nil
	require.NotContains(t, checkpoint.InterruptID2Address, targetID)
	require.NotContains(t, checkpoint.InterruptID2State, targetID)

	clearInterruptContextID(checkpoint.Info.InterruptContexts, targetID)
	chatModelInfo, ok := checkpoint.Info.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.NotNil(t, chatModelInfo)
	require.NotNil(t, chatModelInfo.Info)
	clearInterruptContextID(chatModelInfo.Info.InterruptContexts, targetID)
	require.False(t, interruptContextsContainID(checkpoint.Info.InterruptContexts, targetID))
	require.False(t, interruptContextsContainID(chatModelInfo.Info.InterruptContexts, targetID))

	nested := &compose.InterruptInfo{
		InterruptContexts: []*InterruptCtx{{ID: targetID}},
	}
	setNested(chatModelInfo.Info, nested)
	if checkpoint.InfoDataSourceInterruptID != "" {
		chatModelInfo.Data = nil
	}
	encoded, err := encodeRunnerCheckpoint(&checkpoint)
	require.NoError(t, err)
	return encoded
}

func clearInterruptContextID(contexts []*InterruptCtx, targetID string) {
	visited := make(map[*InterruptCtx]struct{})
	for _, interruptCtx := range contexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			if _, ok := visited[current]; ok {
				break
			}
			visited[current] = struct{}{}
			if current.ID == targetID {
				current.ID = ""
			}
		}
	}
}

func interruptContextsContainID(contexts []*InterruptCtx, targetID string) bool {
	visited := make(map[*InterruptCtx]struct{})
	for _, interruptCtx := range contexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			if _, ok := visited[current]; ok {
				break
			}
			visited[current] = struct{}{}
			if current.ID == targetID {
				return true
			}
		}
	}
	return false
}

func parallelAgentToolLocalAddress(t *testing.T, address Address, parentCallID string) Address {
	t.Helper()
	for i, segment := range address {
		if segment.Type == AddressSegmentTool && segment.ID == "SameChild" &&
			segment.SubID == parentCallID {
			return address[i+1:]
		}
	}
	t.Fatalf("address %q does not contain parent AgentTool call %q", address.String(), parentCallID)
	return nil
}

func resumeCheckpointCompatCandidate(t *testing.T, spec checkpointCompatFixture, raw []byte,
	interruptIDs []string, targetCount, expectedInterrupts int, expectedAddresses []string) {
	t.Helper()
	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), spec.Name, raw))
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent: newCheckpointCompatAgent(t, spec.Depth, spec.ParallelChildren,
			spec.PayloadField, spec.PayloadSize),
		EnableStreaming: spec.Streaming,
		CheckPointStore: store,
	})
	targets := make(map[string]any, targetCount)
	for _, id := range interruptIDs[:targetCount] {
		targets[id] = "resumed"
	}
	iter, err := runner.ResumeWithParams(context.Background(), spec.Name,
		&ResumeParams{Targets: targets})
	require.NoError(t, err)
	outcome := collectCheckpointCompatResumeOutcome(t, iter)
	requireCheckpointCompatResumeOutcome(t, outcome, expectedInterrupts,
		expectedAddresses[targetCount:])
}
