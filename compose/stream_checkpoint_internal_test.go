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
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestStreamCheckpointReplayOwnership(t *testing.T) {
	ch := dagChannelBuilder([]string{"source"}, []string{"source"}, nil, nil).(*dagChannel)
	cm := &channelManager{channels: map[string]channel{"consumer": ch}}
	replay := newStreamCheckpointReplay(cm)
	source := schema.StreamReaderFromArray([]string{"retained"})
	ch.Values["source"] = packStreamReader(source)
	ch.ControlPredecessors["source"] = dependencyStateReady
	ch.DataPredecessors["source"] = true
	ch.retainValues()
	require.NotNil(t, ch.retainedInput)
	ch.closeValues()
	replay.close()
	require.Nil(t, ch.retainedInput)
	require.Nil(t, replay.inputs)
	replay.close()
}

func TestStreamCheckpointReplayRequiresCheckpointID(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			ctx := context.Background()
			wf := NewWorkflow[string, string]()
			var inherited bool
			wf.AddLambdaNode("source", StreamableLambda(func(ctx context.Context, input string) (*schema.StreamReader[string], error) {
				inherited = ctx.Value(streamReplayContextKey{}) == true
				return schema.StreamReaderFromArray([]string{input}), nil
			})).AddInput(START)
			wf.End().AddInput("source")
			store := &streamReplayInternalStore{}
			run, err := wf.Compile(ctx, WithCheckPointStore(store))
			require.NoError(t, err)
			var opts []Option
			if enabled {
				opts = append(opts, WithCheckPointID("enabled"))
			}
			reader, err := run.Stream(ctx, "input", opts...)
			require.NoError(t, err)
			defer reader.Close()
			value, err := reader.Recv()
			require.NoError(t, err)
			require.Equal(t, "input", value)
			_, err = reader.Recv()
			require.ErrorIs(t, err, io.EOF)
			require.Equal(t, enabled, inherited)
		})
	}
}

type streamReplayInternalStore struct{}

func (*streamReplayInternalStore) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}
func (*streamReplayInternalStore) Set(context.Context, string, []byte) error { return nil }
