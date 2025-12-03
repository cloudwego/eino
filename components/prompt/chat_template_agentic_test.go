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

package prompt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestAgenticFormat(t *testing.T) {
	pyFmtTestTemplate := []schema.AgenticMessagesTemplate{
		&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "{context}"}},
			},
		},
		schema.AgenticMessagesPlaceholder("chat_history", true),
	}
	jinja2TestTemplate := []schema.AgenticMessagesTemplate{
		&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "{{context}}"}},
			},
		},
		schema.AgenticMessagesPlaceholder("chat_history", true),
	}
	goFmtTestTemplate := []schema.AgenticMessagesTemplate{
		&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "{{.context}}"}},
			},
		},
		schema.AgenticMessagesPlaceholder("chat_history", true),
	}
	testValues := map[string]any{
		"context": "it's beautiful day",
		"chat_history": []*schema.AgenticMessage{
			{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{
					{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "1"}},
				},
			},
			{
				Role: schema.AgenticRoleTypeUser,
				ContentBlocks: []*schema.ContentBlock{
					{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "2"}},
				},
			},
		},
	}
	expected := []*schema.AgenticMessage{
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "it's beautiful day"}},
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "1"}},
			},
		},
		{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				{Type: schema.ContentBlockTypeUserInputText, UserInputText: &schema.UserInputText{Text: "2"}},
			},
		},
	}

	// FString
	chatTemplate := FromAgenticMessages(schema.FString, pyFmtTestTemplate...)
	msgs, err := chatTemplate.Format(context.Background(), testValues)
	assert.Nil(t, err)
	assert.Equal(t, expected, msgs)

	// Jinja2
	chatTemplate = FromAgenticMessages(schema.Jinja2, jinja2TestTemplate...)
	msgs, err = chatTemplate.Format(context.Background(), testValues)
	assert.Nil(t, err)
	assert.Equal(t, expected, msgs)

	// GoTemplate
	chatTemplate = FromAgenticMessages(schema.GoTemplate, goFmtTestTemplate...)
	msgs, err = chatTemplate.Format(context.Background(), testValues)
	assert.Nil(t, err)
	assert.Equal(t, expected, msgs)
}
