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

func pregelChannelBuilder(_ []string) channel {
	return &pregelChannel{}
}

type pregelChannel struct {
	value any
}

func (ch *pregelChannel) update(_ context.Context, ins map[string]any) error {
	if len(ins) == 0 {
		ch.value = nil
		return nil
	}

	values := make([]any, 0, len(ins))
	for _, v := range ins {
		values = append(values, v)
	}

	if len(values) == 1 {
		ch.value = values[0]
		return nil
	}

	// merge
	v, err := mergeValues(values)
	if err != nil {
		return err
	}

	ch.value = v

	return nil
}

func (ch *pregelChannel) get(_ context.Context) (any, error) {
	if ch.value == nil {
		return nil, fmt.Errorf("pregel channel not ready, value is nil")
	}
	v := ch.value
	ch.value = nil
	return v, nil
}

func (ch *pregelChannel) ready(_ context.Context) bool {
	return ch.value != nil
}

func (ch *pregelChannel) reportSkip(_ []string) (bool, error) {
	return false, nil
}

func (ch *pregelChannel) reportDone(_ string) error { return nil }
