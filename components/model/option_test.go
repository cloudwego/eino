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

package model

import (
	"testing"

	"github.com/smartystreets/goconvey/convey"

	"github.com/cloudwego/eino/schema"
)

func TestOptions(t *testing.T) {
	convey.Convey("test options", t, func() {
		var (
			modelName                  = "model"
			temperature        float32 = 0.9
			maxToken                   = 5000
			topP               float32 = 0.8
			defaultModel               = "default_model"
			defaultTemperature float32 = 1.0
			defaultMaxTokens           = 1000
			defaultTopP        float32 = 0.5
			tools                      = []*schema.ToolInfo{{Name: "asd"}, {Name: "qwe"}}
			toolChoice                 = schema.ToolChoiceForced
			allowedToolNames           = []string{"web_search"}
		)

		opts := GetCommonOptions(
			&Options{
				Model:       &defaultModel,
				Temperature: &defaultTemperature,
				MaxTokens:   &defaultMaxTokens,
				TopP:        &defaultTopP,
			},
			WithModel(modelName),
			WithTemperature(temperature),
			WithMaxTokens(maxToken),
			WithTopP(topP),
			WithStop([]string{"hello", "bye"}),
			WithTools(tools),
			WithToolChoice(toolChoice, allowedToolNames...),
		)

		convey.So(opts, convey.ShouldResemble, &Options{
			Model:            &modelName,
			Temperature:      &temperature,
			MaxTokens:        &maxToken,
			TopP:             &topP,
			Stop:             []string{"hello", "bye"},
			Tools:            tools,
			ToolChoice:       &toolChoice,
			AllowedToolNames: allowedToolNames,
		})
	})

	convey.Convey("test nil tools option", t, func() {
		opts := GetCommonOptions(
			&Options{
				Tools: []*schema.ToolInfo{
					{Name: "asd"},
					{Name: "qwe"},
				},
			},
			WithTools(nil),
		)

		convey.So(opts.Tools, convey.ShouldNotBeNil)
		convey.So(len(opts.Tools), convey.ShouldEqual, 0)
	})
}

type implOption struct {
	userID int64
	name   string
}

func WithUserID(uid int64) Option {
	return WrapImplSpecificOptFn[implOption](func(i *implOption) {
		i.userID = uid
	})
}

func WithName(n string) Option {
	return WrapImplSpecificOptFn[implOption](func(i *implOption) {
		i.name = n
	})
}

func TestImplSpecificOption(t *testing.T) {
	convey.Convey("impl_specific_option", t, func() {
		opt := GetImplSpecificOptions(&implOption{}, WithUserID(101), WithName("Wang"))

		convey.So(opt, convey.ShouldEqual, &implOption{
			userID: 101,
			name:   "Wang",
		})
	})
}
