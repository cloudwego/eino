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
	"fmt"
)

func dagChannelBuilder(dependencies []string) channel {
	waitList := make(map[string]dagChannelState, len(dependencies))
	for _, dep := range dependencies {
		waitList[dep] = unready
	}
	return &dagChannel{
		values:   make(map[string]any),
		waitList: waitList,
	}
}

type dagChannelState int

const (
	unready dagChannelState = iota
	done
	skipped
)

type dagChannel struct {
	values   map[string]any
	waitList map[string]dagChannelState
	value    any
	skipped  bool
	isReady  bool
}

func (ch *dagChannel) update(_ context.Context, ins map[string]any) error {
	if ch.skipped {
		return nil
	}

	for k, v := range ins {
		if _, ok := ch.values[k]; ok {
			return fmt.Errorf("dag channel update, calculate node repeatedly: %s", k)
		}
		ch.values[k] = v
	}

	return nil
}

func (ch *dagChannel) get(_ context.Context) (any, error) {
	if ch.skipped {
		return nil, fmt.Errorf("dag channel has been skipped")
	}
	if !ch.isReady {
		return nil, fmt.Errorf("dag channel not ready, value is nil")
	}
	v := ch.value
	ch.value = nil
	ch.isReady = false

	return v, nil
}

func (ch *dagChannel) ready(_ context.Context) bool {
	if ch.skipped {
		return false
	}
	return ch.isReady
}

func (ch *dagChannel) reportSkip(keys []string) (bool, error) {
	for _, k := range keys {
		if _, ok := ch.waitList[k]; ok {
			ch.waitList[k] = skipped
		}
	}

	allSkipped := true
	for _, state := range ch.waitList {
		if state != skipped {
			allSkipped = false
			break
		}
	}
	ch.skipped = allSkipped

	var err error
	if !allSkipped {
		err = ch.tryUpdateValue()
	}

	return allSkipped, err
}

func (ch *dagChannel) reportDone(key string) error {
	if _, ok := ch.waitList[key]; ok {
		ch.waitList[key] = done
	}

	return ch.tryUpdateValue()
}

func (ch *dagChannel) tryUpdateValue() error {
	for _, state := range ch.waitList {
		if state == unready {
			return nil
		}
	}

	values := mapToList(ch.values)
	if len(values) == 1 {
		ch.value = values[0]
		ch.isReady = true
		return nil
	}
	if len(values) > 1 {
		v, err := mergeValues(values)
		if err != nil {
			return err
		}
		ch.value = v
	}

	ch.isReady = true

	return nil
}
