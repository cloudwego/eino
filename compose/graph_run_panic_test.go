/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestRunnerRunPreservesOriginalPanic(t *testing.T) {
	want := errors.New("original graph panic")
	r := &runner{
		chanSubscribeTo:     make(map[string]*chanCall),
		successors:          make(map[string][]string),
		dataPredecessors:    make(map[string][]string),
		controlPredecessors: make(map[string][]string),
		genericHelper:       &genericHelper{},
		options: graphCompileOptions{
			maxRunSteps: 1,
		},
		runCtx: func(context.Context) context.Context {
			panic(want)
		},
	}
	input := packStreamReader(schema.StreamReaderFromArray([]string{"input"}))

	require.PanicsWithValue(t, want, func() {
		_, _ = r.run(context.Background(), true, input)
	})
}
