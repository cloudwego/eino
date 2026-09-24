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

import "fmt"

func pregelChannelBuilder(controlDependencies []string, _ []string, _ func() any, _ func() streamReader) channel {
	deps := make(map[string]dependencyState, len(controlDependencies))
	for _, dep := range controlDependencies {
		deps[dep] = dependencyStateWaiting
	}
	return &pregelChannel{Values: make(map[string]any), controlPredecessors: deps}
}

type pregelChannel struct {
	Values map[string]any

	mergeConfig FanInMergeConfig

	// controlPredecessors only tracks branch-skip state, so that skip reports
	// can propagate through this node to downstream nodes that wait for all
	// predecessors (see WithTriggerMode). It never affects when this channel
	// fires: any reported value still triggers the node immediately.
	controlPredecessors map[string]dependencyState
}

func (ch *pregelChannel) setMergeConfig(cfg FanInMergeConfig) {
	ch.mergeConfig.StreamMergeWithSourceEOF = cfg.StreamMergeWithSourceEOF
}

func (ch *pregelChannel) load(c channel) error {
	dc, ok := c.(*pregelChannel)
	if !ok {
		return fmt.Errorf("load pregel channel fail, got %T, want *pregelChannel", c)
	}
	ch.Values = dc.Values
	return nil
}

func (ch *pregelChannel) convertValues(fn func(map[string]any) error) error {
	return fn(ch.Values)
}

func (ch *pregelChannel) reportValues(ins map[string]any) error {
	for k, v := range ins {
		ch.Values[k] = v
	}
	return nil
}

func (ch *pregelChannel) get(isStream bool, name string, edgeHandler *edgeHandlerManager) (
	any, bool, error) {
	if len(ch.Values) == 0 {
		return nil, false, nil
	}
	defer func() { ch.Values = map[string]any{} }()
	values := make([]any, len(ch.Values))
	names := make([]string, len(ch.Values))
	i := 0
	for k, v := range ch.Values {
		resolvedV, err := edgeHandler.handle(k, name, v, isStream)
		if err != nil {
			return nil, false, err
		}
		values[i] = resolvedV
		names[i] = k
		i++
	}

	if len(values) == 1 {
		return values[0], true, nil
	}

	// merge
	mergeOpts := &mergeOptions{
		streamMergeWithSourceEOF: ch.mergeConfig.StreamMergeWithSourceEOF,
		names:                    names,
	}
	v, err := mergeValues(values, mergeOpts)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (ch *pregelChannel) reportSkip(keys []string) bool {
	for _, k := range keys {
		if _, ok := ch.controlPredecessors[k]; ok {
			ch.controlPredecessors[k] = dependencyStateSkipped
		}
	}

	// Propagate the skip downstream only when every control predecessor has
	// been skipped, i.e. this node can never fire. Nodes without control
	// predecessors (e.g. reached through data-only edges) never propagate.
	if len(ch.controlPredecessors) == 0 {
		return false
	}
	for _, state := range ch.controlPredecessors {
		if state != dependencyStateSkipped {
			return false
		}
	}
	return true
}
func (ch *pregelChannel) reportDependencies(_ []string) {
	return
}
