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

package adk

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/internal/safe"
)

type concurrentWrapper struct {
	innerAgents     []*flowAgent // assumes more than one agents here
	names           []string
	name            string
	wg              sync.WaitGroup
	mu              sync.Mutex
	hasExit         bool
	lastErr         error
	transferTargets map[string][]string
	interruptInfos  map[string]*InterruptInfo
}

// NewConcurrentWrapper creates a concurrent wrapper for a batch of agents.
// The NAME of the concurrent agent is a sorted list of the names of its agents, wrapped in '[]', separated by comma.
// The inner agents will execute concurrently.
// ALL events produced by the inner agents will be emitted / added to session as usual, with ACTUAL RunPath.
//
// Regarding transfer:
// - inner agents can generate transfer events, but combined together, they cannot transfer to more than one agent.
// - if there are transfer events, the concurrent wrapper will perform the transfer after all agent events are consumed.
// - the inner agents only emits the transfer events, they won't actually perform the transfer.
//
// Regarding interrupt:
// - any of the inner agent can interrupt itself. The other inner agents' executions are not affected by the interrupt.
// - the concurrent wrapper will aggregate all interruptions from all inner agents before emitting an interrupt event.
// - the interrupt event will have RunPath of the concurrent wrapper itself.
//
// Regarding resume:
// - the concurrent wrapper is first resumed with its previous interruptions and emitted transfer targets.
// - all the interrupted agents are resumed concurrently. The other inner agents are treated as finished, so won't be resumed.
//
// NOTE: an error or an intentional EXIT will stop the concurrent wrapper.
// NOTE: it's ok if all inner agents stop naturally (without any transfer or EXIT), the concurrent wrapper will stop naturally too.
// NOTE: concurrent wrapper will first handle interrupt. If interrupt does happen, transfers are not handled this Run.
// NOTE: it's ok if only some of the inner agents transfer, while the other agents stop naturally.
// NOTE: A concurrent wrapper can be used only once (A Run or a Resume both count as one use).
// NOTE: A concurrent wrapper should not be the ROOT agent.
func NewConcurrentWrapper(ctx context.Context, agents []Agent) ResumableAgent {
	names := make([]string, len(agents))
	flowAgents := make([]*flowAgent, len(agents))
	for i, a := range agents {
		names[i] = a.Name(ctx)
		fa := toFlowAgent(ctx, a, withNoExecuteTransfer())
		flowAgents[i] = fa
	}
	return &concurrentWrapper{
		innerAgents: flowAgents,
		names:       names,
		name:        getConcurrentAgentsName(names),
	}
}

func getConcurrentAgentsName(names []string) string {
	// sort the names to get a stable agent name
	sort.Strings(names)
	return "[" + strings.Join(names, ",") + "]"
}

func trySplitConcurrentAgentsName(name string) ([]string, bool) {
	if !strings.HasPrefix(name, "[") || !strings.HasSuffix(name, "]") {
		return nil, false
	}

	return strings.Split(name[1:len(name)-1], ","), true
}

func (c *concurrentWrapper) Name(ctx context.Context) string {
	return c.name
}

func (c *concurrentWrapper) Description(ctx context.Context) string {
	return "wrapper for a batch of concurrently executing agents"
}

type concurrentInterruptInfo struct {
	InterruptInfos  map[string]*InterruptInfo
	TransferTargets map[string][]string
}

func (c *concurrentWrapper) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	defer generator.Close()

	executors := c.genExecutors(ctx, generator, nil, false, options...)

	c.execute(ctx, executors, generator, options...)

	return iterator
}

func (c *concurrentWrapper) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	defer generator.Close()

	if info.InterruptInfo == nil || info.InterruptInfo.Data == nil {
		generator.Send(&AgentEvent{Err: errors.New("interrupt info is nil")})
		return iterator
	}

	intInfo, ok := info.InterruptInfo.Data.(*concurrentInterruptInfo)
	if !ok {
		generator.Send(&AgentEvent{Err: errors.New("interrupt info is not concurrentInterruptInfo")})
		return iterator
	}

	if len(intInfo.InterruptInfos) == 0 { // expects at least one agent was interrupted
		generator.Send(&AgentEvent{Err: errors.New("interrupt info in concurrent wrapper is empty")})
		return iterator
	}

	ctx = rewindConcurrentRunCtx(ctx) // remove this concurrentWrapper from RunPath, so that inner agents can have REAL RunPath

	c.transferTargets = intInfo.TransferTargets

	executors := c.genExecutors(ctx, generator, intInfo, info.EnableStreaming, opts...)

	c.execute(ctx, executors, generator, opts...)

	return iterator
}

func (c *concurrentWrapper) genExecutors(ctx context.Context, generator *AsyncGenerator[*AgentEvent],
	intInfo *concurrentInterruptInfo, enableStreaming bool, options ...AgentRunOption) (executors []func()) {
	toRun := c.innerAgents
	if intInfo != nil {
		toRun = []*flowAgent{}
		for _, a := range c.innerAgents { // only resume those agents that were interrupted
			if intInfo.InterruptInfos[a.Name(ctx)] != nil {
				toRun = append(toRun, a)
			}
		}
	}

	executors = make([]func(), 0, len(toRun))
	for _, a := range toRun {
		fa := toFlowAgent(ctx, a, withNoExecuteTransfer())

		r := func() {
			defer func() {
				panicErr := recover()
				if panicErr != nil {
					e := safe.NewPanicErr(panicErr, debug.Stack())
					generator.Send(&AgentEvent{Err: e})
					c.lastErr = e
				}

				c.wg.Done()
			}()

			var aIter *AsyncIterator[*AgentEvent]
			if intInfo == nil {
				aIter = fa.Run(ctx, nil, filterOptions(fa.Name(ctx), options)...)
			} else {
				subResumeInfo := &ResumeInfo{
					EnableStreaming: enableStreaming,
					InterruptInfo:   intInfo.InterruptInfos[fa.Name(ctx)],
				}
				ctx, _ := initRunCtx(ctx, fa.Name(ctx), nil)
				aIter = fa.Resume(ctx, subResumeInfo, filterOptions(fa.Name(ctx), options)...)
			}

			for {
				event, ok := aIter.Next()
				if !ok {
					break
				}

				if event.Err != nil {
					c.mu.Lock()
					c.lastErr = event.Err
					c.mu.Unlock()
					generator.Send(event)
					break
				}

				if event.Action != nil {
					if event.Action.Exit {
						c.hasExit = true
						generator.Send(event)
						break
					}

					if event.Action.Interrupted != nil {
						c.mu.Lock()
						if c.interruptInfos == nil {
							c.interruptInfos = map[string]*InterruptInfo{}
						}
						c.interruptInfos[fa.Name(ctx)] = event.Action.Interrupted
						c.mu.Unlock()
						break
					}

					if event.Action.TransferToAgent != nil {
						c.mu.Lock()
						if c.transferTargets == nil {
							c.transferTargets = map[string][]string{}
						}
						c.transferTargets[fa.Name(ctx)] = append(c.transferTargets[fa.Name(ctx)], event.Action.TransferToAgent.DestAgentName)
						generator.Send(event)
						c.mu.Unlock()
					}
				} else {
					generator.Send(event)
				}
			}
		}
		executors = append(executors, r)
	}
	return executors
}

func (c *concurrentWrapper) execute(ctx context.Context, executors []func(),
	generator *AsyncGenerator[*AgentEvent], options ...AgentRunOption) {
	for i := 1; i < len(executors); i++ {
		c.wg.Add(1)
		go executors[i]()
	}

	c.wg.Add(1)
	executors[0]()

	c.wg.Wait()

	if c.lastErr != nil || c.hasExit {
		return
	}

	var oneFrom, oneTarget string
	for from, targets := range c.transferTargets {
		if len(targets) > 1 {
			generator.Send(&AgentEvent{
				Err: fmt.Errorf("concurrent wrapper must transfer to only one agent, but found %v", targets)})
			return
		}

		if len(oneTarget) > 0 && oneTarget != targets[0] {
			generator.Send(&AgentEvent{
				Err: fmt.Errorf("concurrent wrapper must transfer to the same agent, but found %s and %s", oneTarget, targets[0])})
			return
		}

		oneTarget = targets[0]
		oneFrom = from
	}

	ctx, runCtx := appendConcurrentRunCtx(ctx, c.names)

	if len(c.interruptInfos) > 0 {
		setConcurrentInterruptRunCtx(ctx, runCtx)
		generator.Send(&AgentEvent{
			AgentName: c.Name(ctx),
			RunPath:   runCtx.RunPath,
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &concurrentInterruptInfo{
						InterruptInfos:  c.interruptInfos,
						TransferTargets: c.transferTargets,
					},
				},
			},
		})
		return
	}

	if len(oneTarget) == 0 { // this concurrent wrapper just naturally stops, without further transfer or direct exit/interrupt
		return
	}

	var fromAgent *flowAgent
	for _, a := range c.innerAgents {
		if a.Name(ctx) == oneFrom {
			fromAgent = a
			break
		}
	}

	if fromAgent == nil {
		panic(fmt.Sprintf("a previous recorded transfer from %s within concurrent wrapper, but the agent not found", oneFrom))
	}

	agentToRun := fromAgent.getAgent(ctx, oneTarget)
	if agentToRun == nil {
		e := errors.New(fmt.Sprintf(
			"transfer failed: agent '%s' not found when transferring from '%s'",
			oneTarget, c.innerAgents[0].Name(ctx)))
		generator.Send(&AgentEvent{Err: e})
		return
	}

	subAIter := agentToRun.Run(ctx, nil /*subagents get input from runCtx*/, options...)
	for {
		subEvent, ok_ := subAIter.Next()
		if !ok_ {
			break
		}

		setAutomaticClose(subEvent)
		generator.Send(subEvent)
	}

	return
}

func appendConcurrentRunCtx(ctx context.Context, agentNames []string) (context.Context, *runContext) {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		runCtx = runCtx.deepCopy()
	} else {
		panic("concurrent wrapper cannot be ROOT agent")
	}

	runCtx.RunPath = append(runCtx.RunPath, ExecutionStep{
		AgentName:  getConcurrentAgentsName(agentNames),
		Concurrent: agentNames,
	})

	return setRunCtx(ctx, runCtx), runCtx
}

func rewindConcurrentRunCtx(ctx context.Context) context.Context {
	runCtx := getRunCtx(ctx)
	if runCtx != nil {
		runCtx = runCtx.deepCopy()
	} else {
		panic("concurrent wrapper cannot be ROOT agent")
	}

	runCtx.RunPath = runCtx.RunPath[:len(runCtx.RunPath)-1]

	return setRunCtx(ctx, runCtx)
}

func setConcurrentInterruptRunCtx(ctx context.Context, interruptRunCtx *runContext) {
	rs := getSession(ctx)
	if rs == nil {
		return
	}

	rs.mtx.Lock()
	rs.interruptRunContexts = []*runContext{interruptRunCtx}
	rs.mtx.Unlock()
}
