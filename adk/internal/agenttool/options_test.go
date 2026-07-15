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

package agenttool

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveEventReceivers_NoTransforms(t *testing.T) {
	receivers := ResolveEventReceivers[int]()
	assert.Len(t, receivers, 0)
}

func TestResolveEventReceivers_TransformsInOptionOrder(t *testing.T) {
	var calls []string
	receivers := ResolveEventReceivers[int](
		WithEventReceiverTransform(func(current []EventReceiver[int]) []EventReceiver[int] {
			require.NotNil(t, current)
			require.Empty(t, current)
			return append(current, func(int) { calls = append(calls, "first") })
		}),
		WithEventReceiverTransform(func(current []EventReceiver[int]) []EventReceiver[int] {
			require.Len(t, current, 1)
			previous := current[0]
			current[0] = func(event int) {
				calls = append(calls, "before")
				previous(event)
			}
			return append(current, func(int) { calls = append(calls, "second") })
		}),
	)

	require.Len(t, receivers, 2)
	for _, receiver := range receivers {
		receiver(1)
	}
	assert.Equal(t, []string{"before", "first", "second"}, calls)
}
