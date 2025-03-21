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

	"github.com/cloudwego/eino/internal/serialization"
)

func dagChannelBuilder(dependencies []string) channel {
	waitList := make(map[string]bool, len(dependencies))
	for _, dep := range dependencies {
		waitList[dep] = false
	}
	return &dagChannel{
		Values:   make(map[string]any),
		WaitList: waitList,
	}
}

type dagChannel struct {
	Values   map[string]any
	WaitList map[string]bool
	Skipped  bool
}

func (ch *dagChannel) add(_ context.Context, ins map[string]any) error {
	if ch.Skipped {
		return nil
	}

	for k, v := range ins {
		if _, ok := ch.Values[k]; ok {
			return fmt.Errorf("dag channel update, calculate node repeatedly: %s", k)
		}
		ch.Values[k] = v
	}
	return nil
}

func (ch *dagChannel) get(_ context.Context) (any, bool, error) {
	if ch.Skipped {
		return nil, false, nil
	}

	var validList []string
	for key, skipped := range ch.WaitList {
		if _, ok := ch.Values[key]; !ok && !skipped {
			return nil, false, nil
		} else if !skipped {
			validList = append(validList, key)
		}
	}

	defer func() {
		ch.Values = make(map[string]any)
	}()

	if len(validList) == 1 {
		return ch.Values[validList[0]], true, nil
	}
	v, err := mergeValues(mapToList(ch.Values))
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

func (ch *dagChannel) reportSkip(keys []string) (bool, error) {
	for _, k := range keys {
		if _, ok := ch.WaitList[k]; ok {
			ch.WaitList[k] = true
		}
	}

	allSkipped := true
	for _, skipped := range ch.WaitList {
		if !skipped {
			allSkipped = false
			break
		}
	}
	ch.Skipped = allSkipped

	return allSkipped, nil
}

func (ch *dagChannel) convertValues(fn func(map[string]any) error) error {
	return fn(ch.Values)
}

func init() {
	serialization.GenericRegister[channel]("_eino_channel")
	serialization.Register("_eino_checkpoint", &checkpoint{})
	serialization.Register("_eino_dag_channel", &dagChannel{})
	serialization.Register("_eino_pregel_channel", &pregelChannel{})
}
