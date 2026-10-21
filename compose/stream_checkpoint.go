/*
 * Copyright 2024 CloudWeGo Authors
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

package compose

import (
	"context"
	"errors"
	"sync"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type streamReplayContextKey struct{}

// streamCheckpointReplay owns inputs already consumed by streaming DAG tasks.
// Returning a reader does not commit its producer: a later Recv can interrupt.
// Keep one replay copy until this execution ends or its output reader closes.
// None of these references are serialized; recovery uses ordinary checkpoint
// channels, inputs and nested checkpoints after materializing their streams.
type streamCheckpointReplay struct {
	channels         *channelManager
	inputs           map[string]any
	outputOwnsReplay bool
	closeOnce        sync.Once
}

func newStreamCheckpointReplay(cm *channelManager) *streamCheckpointReplay {
	for _, channel := range cm.channels {
		if ch, ok := channel.(*dagChannel); ok {
			ch.retainInput = true
		}
	}
	return &streamCheckpointReplay{channels: cm, inputs: make(map[string]any)}
}

func (replay *streamCheckpointReplay) retainTaskInputs(tasks []*task) {
	if replay == nil {
		return
	}
	for _, task := range tasks {
		if previous, ok := replay.inputs[task.nodeKey].(streamReader); ok {
			previous.close()
		}
		if input, ok := task.input.(streamReader); ok {
			copies := input.copy(2)
			task.input, replay.inputs[task.nodeKey] = copies[0], copies[1]
		} else {
			replay.inputs[task.nodeKey] = task.input
		}
	}
}

func (replay *streamCheckpointReplay) release() {
	if !replay.outputOwnsReplay {
		replay.close()
	}
}

func (replay *streamCheckpointReplay) close() {
	replay.closeOnce.Do(func() {
		for _, value := range replay.inputs {
			if reader, ok := value.(streamReader); ok {
				reader.close()
			}
		}
		replay.inputs = nil
		for _, channel := range replay.channels.channels {
			if ch, ok := channel.(*dagChannel); ok && ch.retainedInput != nil {
				ch.retainedInput.closeValues()
				ch.retainedInput = nil
			}
		}
	})
}

// wrapStreamCheckpointOutput transfers replay ownership to the graph output.
// Persist a late interrupt before exposing it to the caller, including when a
// nested graph's stream is forwarded through ordinary transforming nodes.
func (r *runner) wrapStreamCheckpointOutput(ctx context.Context, value any, cm *channelManager,
	checkpointID *string, isSubGraph bool, publish func(*subGraphInterruptError)) any {
	if cm.streamReplay == nil {
		return value
	}
	output, ok := value.(streamReader)
	if !ok {
		return value
	}
	replay := cm.streamReplay
	replay.outputOwnsReplay = true
	return output.withLifecycle(func(streamErr error) error {
		if _, ok := schema.GetSourceName(streamErr); ok {
			return streamErr
		}
		defer replay.close()
		info := newInterruptTempInfo()
		observer := &task{nodeKey: END, ctx: AppendAddressSegment(ctx, AddressSegmentNode, END), err: streamErr}
		if err := r.resolveInterruptCompletedTasks(info, []*task{observer}); err != nil {
			return err
		}
		if len(info.interruptRerunNodes)+len(info.subGraphInterrupts) == 0 {
			return streamErr
		}
		return r.handleInterruptWithSubGraphAndRerunNodes(ctx, info, []*task{observer}, checkpointID,
			isSubGraph, cm, true, publish)
	}, replay.close)
}

// retainValues snapshots source identities before get clears the ready channel.
// The copied readers share chunks until consumed, rather than cloning payloads.
func (ch *dagChannel) retainValues() {
	if ch.retainedInput != nil {
		ch.retainedInput.closeValues()
	}
	saved := &dagChannel{Values: map[string]any{}, ControlPredecessors: map[string]dependencyState{}, DataPredecessors: map[string]bool{}, Skipped: ch.Skipped}
	for key, value := range ch.Values {
		if sr, ok := value.(streamReader); ok {
			copies := sr.copy(2)
			ch.Values[key], saved.Values[key] = copies[0], copies[1]
		} else {
			saved.Values[key] = value
		}
	}
	for key, state := range ch.ControlPredecessors {
		saved.ControlPredecessors[key] = state
	}
	for key, ready := range ch.DataPredecessors {
		saved.DataPredecessors[key] = ready
	}
	ch.retainedInput = saved
}

func (ch *dagChannel) closeValues() {
	for _, value := range ch.Values {
		if reader, ok := value.(streamReader); ok {
			reader.close()
		}
	}
}

// restoreConsumedInputs restores only descendants of interrupted producers.
// Completed siblings replay their data without repeating their execution.
func (replay *streamCheckpointReplay) restoreConsumedInputs(cp *checkpoint) {
	cm := replay.channels
	affected := map[string]bool{}
	queue := append([]string(nil), cp.RerunNodes...)
	for _, key := range queue {
		affected[key] = true
	}
	for i := 0; i < len(queue); i++ {
		for _, next := range cm.successors[queue[i]] {
			if !affected[next] {
				affected[next] = true
				queue = append(queue, next)
			}
		}
	}
	for key := range affected {
		ch, ok := cm.channels[key].(*dagChannel)
		if !ok || ch.retainedInput == nil {
			continue
		}
		// Transfer the retained copy into the ordinary checkpoint channel.
		saved := ch.retainedInput
		ch.retainedInput = nil
		ch.closeValues()
		ch.Values, ch.ControlPredecessors, ch.DataPredecessors, ch.Skipped = saved.Values, saved.ControlPredecessors, saved.DataPredecessors, saved.Skipped
		for from, value := range ch.Values {
			if affected[from] {
				if sr, ok := value.(streamReader); ok {
					sr.close()
				}
				delete(ch.Values, from)
			}
		}
		for from := range ch.ControlPredecessors {
			if affected[from] {
				ch.ControlPredecessors[from] = dependencyStateWaiting
			}
		}
		for from := range ch.DataPredecessors {
			if affected[from] {
				ch.DataPredecessors[from] = false
			}
		}
	}
	for _, key := range cp.RerunNodes {
		if ch, ok := cm.channels[key].(*dagChannel); ok {
			ch.closeValues()
			ch.Values = map[string]any{}
			for from := range ch.ControlPredecessors {
				ch.ControlPredecessors[from] = dependencyStateWaiting
			}
			for from := range ch.DataPredecessors {
				ch.DataPredecessors[from] = false
			}
		}
		if _, has := cp.Inputs[key]; has {
			continue
		}
		if value, ok := replay.inputs[key]; ok {
			cp.Inputs[key] = value
			delete(replay.inputs, key)
		}
	}
}

// collectStreamInterrupts drains retained source inputs so fan-in does not hide
// simultaneous interrupts behind the first error returned to a consumer.
func (r *runner) collectStreamInterrupts(ctx context.Context, ti *interruptTempInfo, cm *channelManager) error {
	queue := make([]string, 0, len(ti.interruptTaskNodes))
	for node := range ti.interruptTaskNodes {
		queue = append(queue, node)
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if seen[node] {
			continue
		}
		seen[node] = true
		ch, ok := cm.channels[node].(*dagChannel)
		if !ok || ch.retainedInput == nil {
			continue
		}
		for from := range ch.retainedInput.Values {
			queue = append(queue, from)
		}
		observer := &task{nodeKey: node, ctx: AppendAddressSegment(ctx, AddressSegmentNode, node)}
		err := r.checkPointer.sc.convertOutputs(true, ch.retainedInput.Values, func(_ string, err error) streamErrorAction {
			if nested := isSubGraphInterrupt(err); nested != nil {
				key := r.interruptOriginNodeKey(observer, nested.signal.Address)
				ti.subGraphInterrupts[key] = nested
				ti.appendSignal(nested.signal)
				return streamErrorAction{ignore: true, dropValue: true}
			}
			var sig *core.InterruptSignal
			if errors.As(err, &sig) {
				ti.appendSignal(sig)
				key := r.interruptOriginNodeKey(observer, sig.Address)
				ti.interruptRerunNodes = appendIfNotExist(ti.interruptRerunNodes, key)
				return streamErrorAction{ignore: true, dropValue: true}
			}
			return streamErrorAction{}
		})
		if err != nil {
			return err
		}
		if err = r.checkPointer.sc.restoreOutputs(true, ch.retainedInput.Values); err != nil {
			return err
		}
	}
	return nil
}

func isStreamRoutingInterrupt(err error) bool {
	_, rerun := IsInterruptRerunError(err)
	return rerun || isSubGraphInterrupt(err) != nil
}

type streamCheckpointTarget struct {
	id         *string
	isSubGraph bool
	publish    func(*subGraphInterruptError)
}

// Branch collection can be the first reader to observe a deferred interrupt.
// Settle concurrent tasks before materializing the same checkpoint as a node error.
func (r *runner) handleStreamRoutingInterrupt(ctx context.Context, completed, pending []*task, tm *taskManager,
	cm *channelManager, target streamCheckpointTarget) error {
	remaining, canceled := tm.waitAll()
	all := append(append(append(completed, remaining...), canceled...), pending...)
	cm.streamReplay.retainTaskInputs(pending)
	info := newInterruptTempInfo()
	for _, t := range append(canceled, pending...) {
		info.interruptRerunNodes = appendIfNotExist(info.interruptRerunNodes, t.nodeKey)
	}
	if err := r.resolveInterruptCompletedTasks(info, all); err != nil {
		return err
	}
	return r.handleInterruptWithSubGraphAndRerunNodes(ctx, info, all, target.id, target.isSubGraph, cm, true, target.publish)
}
