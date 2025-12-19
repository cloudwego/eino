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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/tool"
)

func TestWriteTodos(t *testing.T) {
	m, err := buildBuiltinAgentMiddlewares(false)
	assert.NoError(t, err)

	wt := m[0].AdditionalTools[0].(tool.InvokableTool)

	todos := `[{"content":"content1","status":"pending"},{"content":"content2","status":"pending"}]`
	args := fmt.Sprintf(`{"todos": %s}`, todos)

	result, err := wt.InvokableRun(context.Background(), args)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("Updated todo list to %s", todos), result)
}
