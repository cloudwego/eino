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

package prompt

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestConvPrompt(t *testing.T) {
	assert.NotNil(t, ConvCallbackInput(&CallbackInput{
		AgenticTemplates: []schema.AgenticMessagesTemplate{
			&schema.AgenticMessage{},
		},
	}))
	assert.NotNil(t, ConvCallbackInput(map[string]any{}))
	assert.Nil(t, ConvCallbackInput("asd"))

	assert.NotNil(t, ConvCallbackOutput(&CallbackOutput{
		AgenticResult: []*schema.AgenticMessage{
			{},
		},
		AgenticTemplates: []schema.AgenticMessagesTemplate{
			&schema.AgenticMessage{},
		},
	}))
	assert.NotNil(t, ConvCallbackOutput([]*schema.Message{}))

	agenticResult := []*schema.AgenticMessage{{}}
	out := ConvCallbackOutput(agenticResult)
	assert.NotNil(t, out)
	assert.Equal(t, agenticResult, out.AgenticResult)

	assert.Nil(t, ConvCallbackOutput("asd"))
}
