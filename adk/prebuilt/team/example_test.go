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

package team

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
)

// ExampleNewRunner shows the minimal end-to-end wiring for a team Runner: build a
// RunnerConfig (AgentConfig + TeamConfig + the two required callbacks GenInput and
// OnAgentEvents), construct the Runner, then Push → Run → Wait.
//
// The leader agent is a plain adk.ChatModelAgent; NewRunner automatically injects
// the team middleware (TeamCreate / Agent / SendMessage / TeamDelete tools) and a
// team-aware plantask middleware (the shared task list) into it. The model here is
// a stub so the example is deterministic and needs no API key; in a real program
// supply a live model.Model (e.g. an OpenAI/Ark chat model) instead.
//
// Backend is an interface (see the Backend doc for the durability contract). This
// example uses an in-memory implementation for brevity; production code should use
// a filesystem-backed (or otherwise persistent) Backend whose Write replaces files
// atomically.
func ExampleNewRunner() {
	ctx := context.Background()

	// TeamConfig is purely declarative: where and how team state is stored.
	teamConf := &Config{
		Backend: newInMemoryBackend(),
		BaseDir: "/team-data",
	}

	// The leader agent. In real code, set Model to a live chat model.
	agentConf := &adk.ChatModelAgentConfig{
		Name:        "leader",
		Description: "coordinates the team and delegates work to teammates",
		Model:       &mockBaseChatModel{},
	}

	runnerConf := &RunnerConfig{
		AgentConfig: agentConf,
		TeamConfig:  teamConf,

		// GenInput decides, each turn, which buffered items to process now. The
		// simplest policy consumes everything. It also stops the loop here so the
		// example terminates; a long-running service would instead keep the loop
		// alive and stop it on shutdown.
		GenInput: func(_ context.Context, loop *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			loop.Stop()
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},

		// OnAgentEvents must drain the agent event stream. A handler that needs no
		// events still has to consume them, so a no-op drain is supplied here.
		OnAgentEvents: func(_ context.Context, _ *adk.TurnContext[TurnInput, adk.Message], events *adk.AsyncIterator[*adk.AgentEvent]) error {
			for {
				if _, ok := events.Next(); !ok {
					return nil
				}
			}
		},
	}

	runner, err := NewRunner(ctx, runnerConf)
	if err != nil {
		fmt.Println("new runner:", err)
		return
	}

	// Feed a user message addressed to the leader (empty TargetAgent routes to the
	// leader), start the loop, and wait for it to exit.
	runner.Push(TurnInput{Messages: []string{"Build a small web service."}})
	runner.Run(ctx)
	exit := runner.Wait()

	fmt.Println("runner exited:", exit != nil)
	// Output: runner exited: true
}
