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

package backgroundtask

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func closeWithTimeout(manager *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = manager.Close(ctx)
}

func TestManagerReadOutputDelegatesToStore_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := New(context.Background(), &Config{Store: store})
	defer closeWithTimeout(manager)
	started := createAndStart(t, store, "manager-output")
	_, err := store.AppendOutput(context.Background(), &AppendOutputRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, Data: []byte("record"),
	})
	require.NoError(t, err)

	result, err := manager.ReadOutput(context.Background(), &ReadOutputRequest{
		TaskID: started.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, result.Records, 1)
	require.Equal(t, "record", string(result.Records[0].Data))
}
