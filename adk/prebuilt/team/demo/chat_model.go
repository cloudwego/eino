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

package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudwego/eino-ext/components/model/ark"
	"github.com/cloudwego/eino-ext/components/model/openai"
	arkmodel "github.com/volcengine/volcengine-go-sdk/service/arkruntime/model"

	"github.com/cloudwego/eino/components/model"
)

func NewChatModel(ctx context.Context) (cm model.ToolCallingChatModel, err error) {
	o := &option{}

	if modelName := os.Getenv("ARK_MODEL"); modelName != "" {
		conf := &ark.ChatModelConfig{
			APIKey:      os.Getenv("ARK_API_KEY"),
			BaseURL:     os.Getenv("ARK_BASE_URL"),
			Region:      os.Getenv("ARK_REGION"),
			Model:       modelName,
			MaxTokens:   o.MaxTokens,
			Temperature: o.Temperature,
			TopP:        o.TopP,
		}
		if o.DisableThinking != nil && *o.DisableThinking {
			conf.Thinking = &arkmodel.Thinking{
				Type: arkmodel.ThinkingTypeDisabled,
			}
		}
		if o.JsonSchema != nil {
			conf.ResponseFormat = &ark.ResponseFormat{
				Type: arkmodel.ResponseFormatJSONSchema,
				JSONSchema: &arkmodel.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:        o.JsonSchema.Name,
					Description: o.JsonSchema.Description,
					Schema:      o.JsonSchema.JSONSchema,
					Strict:      o.JsonSchema.Strict,
				},
			}
		}
		cm, err = ark.NewChatModel(ctx, conf)

	} else if modelName = os.Getenv("OPENAI_MODEL"); modelName != "" {
		conf := &openai.ChatModelConfig{
			APIKey: os.Getenv("OPENAI_API_KEY"),
			ByAzure: func() bool {
				return os.Getenv("OPENAI_BY_AZURE") == "true"
			}(),
			BaseURL:     os.Getenv("OPENAI_BASE_URL"),
			Model:       modelName,
			MaxTokens:   o.MaxTokens,
			Temperature: o.Temperature,
			TopP:        o.TopP,
		}
		if o.JsonSchema != nil {
			conf.ResponseFormat = &openai.ChatCompletionResponseFormat{
				Type:       openai.ChatCompletionResponseFormatTypeJSONSchema,
				JSONSchema: o.JsonSchema,
			}
		}
		cm, err = openai.NewChatModel(ctx, conf)
	}
	if err != nil {
		return nil, err
	}
	if cm == nil {
		return nil, fmt.Errorf("no model config")
	}

	return cm, nil
}

type option struct {
	MaxTokens       *int
	Temperature     *float32
	TopP            *float32
	DisableThinking *bool
	JsonSchema      *openai.ChatCompletionResponseFormatJSONSchema
}
