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
	"fmt"
)

func dagChannelBuilder(controlDependencies []string, dataDependencies []string, zeroValue func() any, emptyStream func() streamReader) channel {
	deps := make(map[string]dependencyState, len(controlDependencies))
	for _, dep := range controlDependencies {
		deps[dep] = dependencyStateWaiting
	}
	indirect := make(map[string]bool, len(dataDependencies))
	for _, dep := range dataDependencies {
		indirect[dep] = false
	}

	return &dagChannel{
		Values:              make(map[string]any),
		ControlPredecessors: deps,
		DataPredecessors:    indirect,
		zeroValue:           zeroValue,
		emptyStream:         emptyStream,
	}
}

type dependencyState uint8

const (
	dependencyStateWaiting dependencyState = iota
	dependencyStateReady
	dependencyStateSkipped
)

type dagChannel struct {
	zeroValue   func() any
	emptyStream func() streamReader

	ControlPredecessors map[string]dependencyState
	Values              map[string]any
	DataPredecessors    map[string]bool // if all dependencies have been skipped, indirect dependencies won't effect.
	Skipped             bool

	mergeConfig FanInMergeConfig
}

func (ch *dagChannel) setMergeConfig(cfg FanInMergeConfig) {
	ch.mergeConfig.StreamMergeWithSourceEOF = cfg.StreamMergeWithSourceEOF
}

func (ch *dagChannel) load(c channel) error {
	dc, ok := c.(*dagChannel)
	if !ok {
		return fmt.Errorf("load dag channel fail, got %T, want *dagChannel", c)
	}
	ch.ControlPredecessors = dc.ControlPredecessors
	ch.DataPredecessors = dc.DataPredecessors
	ch.Skipped = dc.Skipped
	ch.Values = dc.Values
	return nil
}

func (ch *dagChannel) reportValues(ins map[string]any) error {
	if ch.Skipped {
		return nil
	}

	for k, v := range ins {
		if _, ok := ch.DataPredecessors[k]; !ok {
			continue
		}
		ch.DataPredecessors[k] = true
		ch.Values[k] = v
	}
	return nil
}

func (ch *dagChannel) reportDependencies(dependencies []string) {
	if ch.Skipped {
		return
	}

	for _, dep := range dependencies {
		if _, ok := ch.ControlPredecessors[dep]; ok {
			ch.ControlPredecessors[dep] = dependencyStateReady
		}
	}
	return
}

func (ch *dagChannel) reportSkip(keys []string) bool {
	for _, k := range keys {
		if _, ok := ch.ControlPredecessors[k]; ok {
			ch.ControlPredecessors[k] = dependencyStateSkipped
		}
		if _, ok := ch.DataPredecessors[k]; ok {
			ch.DataPredecessors[k] = true
		}
	}

	allSkipped := true
	for _, state := range ch.ControlPredecessors {
		if state != dependencyStateSkipped {
			allSkipped = false
			break
		}
	}
	ch.Skipped = allSkipped

	return allSkipped
}

func (ch *dagChannel) get(isStream bool, name string, edgeHandler *edgeHandlerManager) (
	any, bool, error) {
	if ch.Skipped {
		return nil, false, nil
	}

	if len(ch.ControlPredecessors)+len(ch.DataPredecessors) == 0 {
		return nil, false, nil
	}

	for _, state := range ch.ControlPredecessors {
		if state == dependencyStateWaiting {
			return nil, false, nil
		}
	}
	for _, ready := range ch.DataPredecessors {
		if !ready {
			return nil, false, nil
		}
	}

	defer func() {
		ch.Values = make(map[string]any)
		for k := range ch.ControlPredecessors {
			ch.ControlPredecessors[k] = dependencyStateWaiting
		}
		for k := range ch.DataPredecessors {
			ch.DataPredecessors[k] = false
		}
	}()

	valueList := make([]any, len(ch.Values))
	names := make([]string, len(ch.Values))
	i := 0
	for k, value := range ch.Values {
		resolvedV, err := edgeHandler.handle(k, name, value, isStream)
		if err != nil {
			return nil, false, err
		}
		valueList[i] = resolvedV
		names[i] = k
		i++
	}

	if len(valueList) == 0 {
		if isStream {
			return ch.emptyStream(), true, nil
		}
		return ch.zeroValue(), true, nil
	}
	if len(valueList) == 1 {
		return valueList[0], true, nil
	}

	mergeOpts := &mergeOptions{
		streamMergeWithSourceEOF: ch.mergeConfig.StreamMergeWithSourceEOF,
		names:                    names,
	}
	v, err := mergeValues(valueList, mergeOpts)
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (ch *dagChannel) convertValues(fn func(map[string]any) error) error {
	return fn(ch.Values)
}
