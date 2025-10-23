/*
 * Copyright 2025 CloudWeGo Authors
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

package deep

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWriteTodosTool(t *testing.T) {
	wtt, err := newWriteTodosTool()
	assert.NoError(t, err)

	_, err = wtt.InvokableRun(context.Background(), `{
	"todos": [
		{
			"content": "1",
			"status": "completed"
		},
		{
			"content": "2",
			"status": "in_progress"
		},
		{
			"content": "3",
			"status": "pending"
		}
	]
}`)
	assert.NoError(t, err)
}
