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

package embedding

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
)

func TestConvEmbedding(t *testing.T) {
	assert.NotNil(t, ConvCallbackInput(&CallbackInput{}))
	assert.NotNil(t, ConvCallbackInput([]string{}))
	assert.NotNil(t, ConvCallbackInput([]MultiModalInput{
		{
			{Type: schema.ChatMessagePartTypeText, Text: "hello"},
		},
	}))
	assert.NotNil(t, ConvCallbackInput([][]schema.MessageInputPart{
		{
			{Type: schema.ChatMessagePartTypeText, Text: "hello"},
		},
	}))
	assert.NotNil(t, ConvCallbackInput([]schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "world"},
	}))
	assert.Nil(t, ConvCallbackInput("asd"))

	assert.NotNil(t, ConvCallbackOutput(&CallbackOutput{}))
	assert.NotNil(t, ConvCallbackOutput([][]float64{}))
	assert.Nil(t, ConvCallbackOutput("asd"))
}
