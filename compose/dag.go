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
	return &dagChannel{
		values:   make(map[string]any),
		waitList: dependencies,
	}
}

type dagChannel struct {
	values   map[string]any
	waitList []string
	value    any
}

func (ch *dagChannel) update(ctx context.Context, ins map[string]any) error {
	for k, v := range ins {
		if _, ok := ch.values[k]; ok {
			return fmt.Errorf("dag channel update, calculate node repeatedly: %s", k)
		}
		ch.values[k] = v
	}

	for i := range ch.waitList {
		if _, ok := ch.values[ch.waitList[i]]; !ok {
			return nil
		}
	}

	if len(ch.waitList) == 1 {
		ch.value = ch.values[ch.waitList[0]]
		return nil
	}
	v, err := mergeValues(mapToList(ch.values))
	if err != nil {
		return fmt.Errorf("dag channel merge value fail: %w", err)
	}
	ch.value = v

	return nil
}

func (ch *dagChannel) get(ctx context.Context) (any, error) {
	if ch.value == nil {
		return nil, fmt.Errorf("dag channel not ready, value is nil")
	}
	v := ch.value
	ch.value = nil
	return v, nil
}

func (ch *dagChannel) ready(ctx context.Context) bool {
	return ch.value != nil
}
