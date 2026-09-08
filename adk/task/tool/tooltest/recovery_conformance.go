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

// Package tooltest provides conformance checks for managed background tool
// implementations.
package tooltest

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	backgroundtool "github.com/cloudwego/eino/adk/task/tool"
)

// RecoverySnapshot exposes backend identity and replay state to the reusable
// recovery conformance check. LogicalOperationID is backend-private test data;
// production task APIs continue to expose only the Eino task ID.
type RecoverySnapshot struct {
	LogicalOperationID string
	Updates            []*backgroundtool.Update
}

// RecoveryConformanceConfig configures CheckRecoveryConformance.
type RecoveryConformanceConfig struct {
	TaskID    string
	Arguments string
	NewTool   func() backgroundtool.RecoverableTool
	Snapshot  func(context.Context, string) (*RecoverySnapshot, error)
}

// CheckRecoveryConformance verifies that independent adapter instances share
// one logical operation and replay stable update identities. Backend suites may
// add retention-specific assertions around this common check.
func CheckRecoveryConformance(
	ctx context.Context,
	config *RecoveryConformanceConfig,
) error {
	if config == nil || config.TaskID == "" || config.Arguments == "" ||
		config.NewTool == nil || config.Snapshot == nil {
		return errors.New("task/tool/tooltest: complete recovery conformance config is required")
	}
	first := config.NewTool()
	second := config.NewTool()
	third := config.NewTool()
	if first == nil || second == nil || third == nil {
		return errors.New("task/tool/tooltest: recovery factory returned nil")
	}
	if err := first.ValidateArguments(config.Arguments); err != nil {
		return fmt.Errorf("task/tool/tooltest: validate conformance arguments: %w", err)
	}
	firstResult, err := first.Start(ctx, &backgroundtool.StartRequest{
		TaskID: config.TaskID, Arguments: config.Arguments, Attempt: 1,
	})
	if err != nil {
		return fmt.Errorf("task/tool/tooltest: first start: %w", err)
	}
	if firstResult == nil || firstResult.Run == nil {
		return errors.New("task/tool/tooltest: first start returned nil run")
	}
	before, err := config.Snapshot(ctx, config.TaskID)
	if err != nil {
		return fmt.Errorf("task/tool/tooltest: snapshot after first start: %w", err)
	}
	duplicateResult, err := second.Start(ctx, &backgroundtool.StartRequest{
		TaskID: config.TaskID, Arguments: config.Arguments, Attempt: 1,
	})
	if err != nil {
		return fmt.Errorf("task/tool/tooltest: duplicate start: %w", err)
	}
	if duplicateResult == nil || duplicateResult.Run == nil {
		return errors.New("task/tool/tooltest: duplicate start returned nil run")
	}
	if !bytes.Equal(firstResult.Checkpoint, duplicateResult.Checkpoint) {
		return errors.New(
			"task/tool/tooltest: duplicate start changed checkpoint",
		)
	}
	afterDuplicate, err := config.Snapshot(ctx, config.TaskID)
	if err != nil {
		return fmt.Errorf("task/tool/tooltest: snapshot after duplicate start: %w", err)
	}
	if err = compareRecoverySnapshots(before, afterDuplicate); err != nil {
		return fmt.Errorf("task/tool/tooltest: duplicate start: %w", err)
	}
	recoveredRun, err := third.Recover(ctx, &backgroundtool.RecoverRequest{
		TaskID: config.TaskID, Arguments: config.Arguments, Attempt: 2,
		Checkpoint: append([]byte(nil), firstResult.Checkpoint...),
	})
	if err != nil {
		return fmt.Errorf("task/tool/tooltest: recover: %w", err)
	}
	if recoveredRun == nil {
		return errors.New("task/tool/tooltest: recover returned nil run")
	}
	afterRecover, err := config.Snapshot(ctx, config.TaskID)
	if err != nil {
		return fmt.Errorf("task/tool/tooltest: snapshot after recover: %w", err)
	}
	if err = compareRecoverySnapshots(before, afterRecover); err != nil {
		return fmt.Errorf("task/tool/tooltest: recover: %w", err)
	}
	if err = recoveredRun.Stop(ctx); err != nil {
		return fmt.Errorf("task/tool/tooltest: stop recovered operation: %w", err)
	}
	return nil
}

func compareRecoverySnapshots(expected, actual *RecoverySnapshot) error {
	if expected == nil || actual == nil || expected.LogicalOperationID == "" {
		return errors.New("backend snapshot requires a logical operation id")
	}
	if actual.LogicalOperationID != expected.LogicalOperationID {
		return fmt.Errorf(
			"logical operation changed from %q to %q",
			expected.LogicalOperationID, actual.LogicalOperationID,
		)
	}
	if len(actual.Updates) < len(expected.Updates) {
		return errors.New("recovered update history lost records")
	}
	for i, update := range expected.Updates {
		replayed := actual.Updates[i]
		if update == nil || replayed == nil || update.EventID == "" ||
			update.EventID != replayed.EventID || !bytes.Equal(update.Data, replayed.Data) {
			return fmt.Errorf("update %d did not preserve event identity and bytes", i)
		}
	}
	return nil
}
