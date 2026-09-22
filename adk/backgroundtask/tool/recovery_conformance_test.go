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
	"testing"

	"github.com/stretchr/testify/require"
)

type conformanceToolStub struct {
	validateErr        error
	start              func() (Run, error)
	startCheckpoint    []byte
	recover            func() (Run, error)
	recoverWithRequest func(*RecoverRequest) (Run, error)
}

func (t *conformanceToolStub) ValidateArguments(string) error { return t.validateErr }
func (t *conformanceToolStub) Start(context.Context, *StartRequest) (*StartResult, error) {
	run, err := t.start()
	if err != nil {
		return nil, err
	}
	return &StartResult{
		Run: run, Checkpoint: append([]byte(nil), t.startCheckpoint...),
	}, nil
}
func (t *conformanceToolStub) Recover(_ context.Context, request *RecoverRequest) (Run, error) {
	if t.recoverWithRequest != nil {
		return t.recoverWithRequest(request)
	}
	return t.recover()
}

type conformanceRunStub struct {
	stopErr error
}

func (*conformanceRunStub) Wait(context.Context) (*Outcome, error) {
	return nil, nil
}
func (r *conformanceRunStub) Stop(context.Context) error { return r.stopErr }

func conformanceConfig(tools []RecoverableBackgroundTool, snapshots []*recoverySnapshot, snapshotErrAt int) *recoveryConformanceConfig {
	toolIndex := 0
	snapshotIndex := 0
	return &recoveryConformanceConfig{
		TaskID: "task", Arguments: `{"value":"x"}`,
		NewTool: func() RecoverableBackgroundTool {
			tool := tools[toolIndex]
			toolIndex++
			return tool
		},
		Snapshot: func(context.Context, string) (*recoverySnapshot, error) {
			if snapshotIndex == snapshotErrAt {
				return nil, errors.New("snapshot failed")
			}
			snapshot := snapshots[snapshotIndex]
			snapshotIndex++
			return snapshot, nil
		},
	}
}

func healthyConformanceTools(stopErr error) []RecoverableBackgroundTool {
	newTool := func() RecoverableBackgroundTool {
		return &conformanceToolStub{
			start: func() (Run, error) {
				return &conformanceRunStub{}, nil
			},
			recover: func() (Run, error) {
				return &conformanceRunStub{stopErr: stopErr}, nil
			},
		}
	}
	return []RecoverableBackgroundTool{newTool(), newTool(), newTool()}
}

func stableSnapshots() []*recoverySnapshot {
	update := &Update{EventID: "event", Data: []byte("same")}
	return []*recoverySnapshot{
		{LogicalOperationID: "operation", Updates: []*Update{update}},
		{LogicalOperationID: "operation", Updates: []*Update{cloneConformanceUpdate(update)}},
		{LogicalOperationID: "operation", Updates: []*Update{cloneConformanceUpdate(update)}},
	}
}

func cloneConformanceUpdate(update *Update) *Update {
	if update == nil {
		return nil
	}
	cloned := *update
	cloned.Data = append([]byte(nil), update.Data...)
	return &cloned
}

func TestCheckRecoveryConformance(t *testing.T) {
	require.NoError(t, checkRecoveryConformance(
		context.Background(),
		conformanceConfig(healthyConformanceTools(nil), stableSnapshots(), -1),
	))

	for _, config := range []*recoveryConformanceConfig{
		nil,
		{},
		{
			TaskID: "task", Arguments: "{}",
			NewTool: func() RecoverableBackgroundTool { return nil },
			Snapshot: func(context.Context, string) (*recoverySnapshot, error) {
				return nil, nil
			},
		},
	} {
		require.Error(t, checkRecoveryConformance(context.Background(), config))
	}
}

func TestCheckRecoveryConformancePassesStableCheckpoint(t *testing.T) {
	checkpoint := []byte(`{"run_id":"business-run"}`)
	newStartingTool := func() RecoverableBackgroundTool {
		return &conformanceToolStub{
			startCheckpoint: append([]byte(nil), checkpoint...),
			start: func() (Run, error) {
				return &conformanceRunStub{}, nil
			},
		}
	}
	recovering := &conformanceToolStub{
		recoverWithRequest: func(request *RecoverRequest) (Run, error) {
			require.Equal(t, checkpoint, request.Checkpoint)
			return &conformanceRunStub{}, nil
		},
	}
	require.NoError(t, checkRecoveryConformance(
		context.Background(),
		conformanceConfig(
			[]RecoverableBackgroundTool{
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
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "validate conformance arguments")
	})
	t.Run("first start", func(t *testing.T) {
		tools := healthyConformanceTools(nil)
		tools[0].(*conformanceToolStub).start = func() (Run, error) {
			return nil, errors.New("start failed")
		}
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "first start")
		tools = healthyConformanceTools(nil)
		tools[0].(*conformanceToolStub).start = func() (Run, error) {
			return nil, nil
		}
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "nil run")
	})
	t.Run("snapshots", func(t *testing.T) {
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(),
			conformanceConfig(healthyConformanceTools(nil), stableSnapshots(), 0),
		), "snapshot after first start")
		snapshots := stableSnapshots()
		snapshots[1].LogicalOperationID = "duplicate"
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(),
			conformanceConfig(healthyConformanceTools(nil), snapshots, -1),
		), "logical operation changed")
		snapshots = stableSnapshots()
		snapshots[2].Updates = nil
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(),
			conformanceConfig(healthyConformanceTools(nil), snapshots, -1),
		), "lost records")
	})
	t.Run("duplicate start", func(t *testing.T) {
		tools := healthyConformanceTools(nil)
		tools[1].(*conformanceToolStub).start = func() (Run, error) {
			return nil, errors.New("duplicate failed")
		}
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "duplicate start")
		tools = healthyConformanceTools(nil)
		tools[1].(*conformanceToolStub).start = func() (Run, error) {
			return nil, nil
		}
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "nil run")
	})
	t.Run("recover and stop", func(t *testing.T) {
		tools := healthyConformanceTools(nil)
		tools[2].(*conformanceToolStub).recover = func() (Run, error) {
			return nil, errors.New("recover failed")
		}
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "recover")
		tools = healthyConformanceTools(nil)
		tools[2].(*conformanceToolStub).recover = func() (Run, error) {
			return nil, nil
		}
		require.ErrorContains(t, checkRecoveryConformance(
			context.Background(), conformanceConfig(tools, stableSnapshots(), -1),
		), "nil run")
		require.ErrorContains(t, checkRecoveryConformance(
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
		&recoverySnapshot{
			LogicalOperationID: "operation",
			Updates:            []*Update{nil},
		},
		&recoverySnapshot{
			LogicalOperationID: "operation",
			Updates:            []*Update{nil},
		},
	))
	require.Error(t, compareRecoverySnapshots(
		&recoverySnapshot{
			LogicalOperationID: "operation",
			Updates: []*Update{{
				EventID: "event", Data: []byte("one"),
			}},
		},
		&recoverySnapshot{
			LogicalOperationID: "operation",
			Updates: []*Update{{
				EventID: "event", Data: []byte("two"),
			}},
		},
	))
}
