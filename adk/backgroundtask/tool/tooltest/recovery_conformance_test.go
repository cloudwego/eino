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

package tooltest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	backgroundtool "github.com/cloudwego/eino/adk/backgroundtask/tool"
)

type conformanceToolStub struct {
	validateErr        error
	start              func() (backgroundtool.Run, error)
	startCheckpoint    []byte
	recover            func() (backgroundtool.Run, error)
	recoverWithRequest func(*backgroundtool.RecoverRequest) (backgroundtool.Run, error)
}

func (t *conformanceToolStub) ValidateArguments(string) error { return t.validateErr }
func (t *conformanceToolStub) Start(
	context.Context,
	*backgroundtool.StartRequest,
) (*backgroundtool.StartResult, error) {
	run, err := t.start()
	if err != nil {
		return nil, err
	}
	return &backgroundtool.StartResult{
		Run: run, Checkpoint: append([]byte(nil), t.startCheckpoint...),
	}, nil
}
func (t *conformanceToolStub) Recover(
	_ context.Context,
	request *backgroundtool.RecoverRequest,
) (backgroundtool.Run, error) {
	if t.recoverWithRequest != nil {
		return t.recoverWithRequest(request)
	}
	return t.recover()
}

type conformanceRunStub struct {
	stopErr error
}

func (*conformanceRunStub) Wait(context.Context) (*backgroundtool.Outcome, error) {
	return nil, nil
}
func (r *conformanceRunStub) Stop(context.Context) error { return r.stopErr }

func conformanceConfig(
	tools []backgroundtool.RecoverableBackgroundTool,
	snapshots []*RecoverySnapshot,
	snapshotErrAt int,
) *RecoveryConformanceConfig {
	toolIndex := 0
	snapshotIndex := 0
	return &RecoveryConformanceConfig{
		TaskID: "task", Arguments: `{"value":"x"}`,
		NewTool: func() backgroundtool.RecoverableBackgroundTool {
			tool := tools[toolIndex]
			toolIndex++
			return tool
		},
		Snapshot: func(context.Context, string) (*RecoverySnapshot, error) {
			if snapshotIndex == snapshotErrAt {
				return nil, errors.New("snapshot failed")
			}
			snapshot := snapshots[snapshotIndex]
			snapshotIndex++
			return snapshot, nil
		},
	}
}

func healthyConformanceTools(stopErr error) []backgroundtool.RecoverableBackgroundTool {
	newTool := func() backgroundtool.RecoverableBackgroundTool {
		return &conformanceToolStub{
			start: func() (backgroundtool.Run, error) {
				return &conformanceRunStub{}, nil
			},
			recover: func() (backgroundtool.Run, error) {
				return &conformanceRunStub{stopErr: stopErr}, nil
			},
		}
	}
	return []backgroundtool.RecoverableBackgroundTool{newTool(), newTool(), newTool()}
}

func stableSnapshots() []*RecoverySnapshot {
	update := &backgroundtool.Update{EventID: "event", Data: []byte("same")}
	return []*RecoverySnapshot{
		{LogicalOperationID: "operation", Updates: []*backgroundtool.Update{update}},
		{LogicalOperationID: "operation", Updates: []*backgroundtool.Update{cloneUpdate(update)}},
		{LogicalOperationID: "operation", Updates: []*backgroundtool.Update{cloneUpdate(update)}},
	}
}

func cloneUpdate(update *backgroundtool.Update) *backgroundtool.Update {
	if update == nil {
		return nil
	}
	cloned := *update
	cloned.Data = append([]byte(nil), update.Data...)
	return &cloned
}

func TestCheckRecoveryConformance(t *testing.T) {
	require.NoError(t, CheckRecoveryConformance(
		context.Background(),
		conformanceConfig(healthyConformanceTools(nil), stableSnapshots(), -1),
	))

	for _, config := range []*RecoveryConformanceConfig{
		nil,
		{},
		{
			TaskID: "task", Arguments: "{}",
			NewTool: func() backgroundtool.RecoverableBackgroundTool { return nil },
			Snapshot: func(context.Context, string) (*RecoverySnapshot, error) {
				return nil, nil
			},
		},
	} {
		require.Error(t, CheckRecoveryConformance(context.Background(), config))
	}
}

func TestCheckRecoveryConformancePassesStableCheckpoint(t *testing.T) {
	checkpoint := []byte(`{"run_id":"business-run"}`)
	newStartingTool := func() backgroundtool.RecoverableBackgroundTool {
		return &conformanceToolStub{
			startCheckpoint: append([]byte(nil), checkpoint...),
			start: func() (backgroundtool.Run, error) {
				return &conformanceRunStub{}, nil
			},
		}
	}
	recovering := &conformanceToolStub{
		recoverWithRequest: func(
			request *backgroundtool.RecoverRequest,
		) (backgroundtool.Run, error) {
			require.Equal(t, checkpoint, request.Checkpoint)
			return &conformanceRunStub{}, nil
		},
	}
	require.NoError(t, CheckRecoveryConformance(
		context.Background(),
		conformanceConfig(
			[]backgroundtool.RecoverableBackgroundTool{
				newStartingTool(),
				newStartingTool(),
				recovering,
			},
			stableSnapshots(),
			-1,
		),
	))
}

func TestCheckRecoveryConformanceFailures(t *testing.T) {
	t.Run("validate", func(t *testing.T) {
		tools := healthyConformanceTools(nil)
		tools[0].(*conformanceToolStub).validateErr = errors.New("invalid")
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "validate conformance arguments")
	})
	t.Run("first start", func(t *testing.T) {
		tools := healthyConformanceTools(nil)
		tools[0].(*conformanceToolStub).start = func() (backgroundtool.Run, error) {
			return nil, errors.New("start failed")
		}
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "first start")
		tools = healthyConformanceTools(nil)
		tools[0].(*conformanceToolStub).start = func() (backgroundtool.Run, error) {
			return nil, nil
		}
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "nil run")
	})
	t.Run("snapshots", func(t *testing.T) {
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(),
			conformanceConfig(healthyConformanceTools(nil), stableSnapshots(), 0),
		), "snapshot after first start")
		snapshots := stableSnapshots()
		snapshots[1].LogicalOperationID = "duplicate"
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(),
			conformanceConfig(healthyConformanceTools(nil), snapshots, -1),
		), "logical operation changed")
		snapshots = stableSnapshots()
		snapshots[2].Updates = nil
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(),
			conformanceConfig(healthyConformanceTools(nil), snapshots, -1),
		), "lost records")
	})
	t.Run("duplicate start", func(t *testing.T) {
		tools := healthyConformanceTools(nil)
		tools[1].(*conformanceToolStub).start = func() (backgroundtool.Run, error) {
			return nil, errors.New("duplicate failed")
		}
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "duplicate start")
		tools = healthyConformanceTools(nil)
		tools[1].(*conformanceToolStub).start = func() (backgroundtool.Run, error) {
			return nil, nil
		}
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "nil run")
	})
	t.Run("recover and stop", func(t *testing.T) {
		tools := healthyConformanceTools(nil)
		tools[2].(*conformanceToolStub).recover = func() (backgroundtool.Run, error) {
			return nil, errors.New("recover failed")
		}
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "recover")
		tools = healthyConformanceTools(nil)
		tools[2].(*conformanceToolStub).recover = func() (backgroundtool.Run, error) {
			return nil, nil
		}
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "nil run")
		require.ErrorContains(t, CheckRecoveryConformance(
			context.Background(),
			conformanceConfig(
				healthyConformanceTools(errors.New("stop failed")),
				stableSnapshots(),
				-1,
			),
		), "stop recovered operation")
	})
}

func TestCompareRecoverySnapshotsRejectsInvalidUpdates(t *testing.T) {
	require.Error(t, compareRecoverySnapshots(nil, nil))
	require.Error(t, compareRecoverySnapshots(
		&RecoverySnapshot{
			LogicalOperationID: "operation",
			Updates:            []*backgroundtool.Update{nil},
		},
		&RecoverySnapshot{
			LogicalOperationID: "operation",
			Updates:            []*backgroundtool.Update{nil},
		},
	))
	require.Error(t, compareRecoverySnapshots(
		&RecoverySnapshot{
			LogicalOperationID: "operation",
			Updates: []*backgroundtool.Update{{
				EventID: "event", Data: []byte("one"),
			}},
		},
		&RecoverySnapshot{
			LogicalOperationID: "operation",
			Updates: []*backgroundtool.Update{{
				EventID: "event", Data: []byte("two"),
			}},
		},
	))
}
