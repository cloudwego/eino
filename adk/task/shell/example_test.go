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

package shell_test

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk/task"
	backgroundshell "github.com/cloudwego/eino/adk/task/shell"
	backgroundtool "github.com/cloudwego/eino/adk/task/tool"
	"github.com/cloudwego/eino/schema"
)

type remoteShell struct{}

func (remoteShell) StartCommand(
	context.Context,
	*backgroundshell.StartCommandRequest,
) (backgroundtool.Run, error) {
	return commandRun{}, nil
}
func (remoteShell) RecoverCommand(
	context.Context,
	*backgroundshell.RecoverCommandRequest,
) (backgroundtool.Run, error) {
	return commandRun{}, nil
}

type commandRun struct{}

func (commandRun) Wait(context.Context) (*backgroundtool.Outcome, error) {
	return &backgroundtool.Outcome{
		Status: task.OutcomeCompleted, Data: []byte("ok"),
	}, nil
}
func (commandRun) Stop(context.Context) error { return nil }

func ExampleNewRegistration() {
	registration, _ := backgroundshell.NewRegistration(&backgroundshell.RegistrationConfig{
		Info: &schema.ToolInfo{
			Name: "execute", Desc: "Run a recoverable remote command",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"command": {Type: schema.String, Required: true},
			}),
		},
		Shell: remoteShell{},
	})
	fmt.Println(registration.Info.Name)
	// Output: execute
}
