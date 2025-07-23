package adk

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type ConcurrentWrapper struct {
	Agents []Agent // assumes more than one agents here
}

func (c *ConcurrentWrapper) Name(ctx context.Context) string {
	names := make([]string, 0, len(c.Agents))
	for _, agent := range c.Agents {
		names = append(names, agent.Name(ctx))
	}
	return "[" + strings.Join(names, ",") + "]"
}

func (c *ConcurrentWrapper) Description(ctx context.Context) string {
	return "wrapper for a batch of concurrently executing agents"
}

type concurrentInterruptInfo struct {
	InterruptInfos  map[string]*InterruptInfo
	TransferTargets map[string][]string
}

func (c *ConcurrentWrapper) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	defer generator.Close()

	names := make([]string, len(c.Agents))
	for i, a := range c.Agents {
		names[i] = a.Name(ctx)
	}

	ctx, runCtx := initConcurrentRunCtx(ctx, names, input)

	var (
		exit            bool
		lastErr         error
		wg              = sync.WaitGroup{}
		transferTargets = map[string][]string{}
		mu              = sync.Mutex{}
		interruptInfos  map[string]*InterruptInfo
	)

	r := func(fa *flowAgent) {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
				lastErr = e
			}

			wg.Done()
		}()

		aIter := fa.Run(ctx, input, filterOptions(fa.Name(ctx), options)...)

		for {
			event, ok := aIter.Next()
			if !ok {
				break
			}

			if event.Err != nil {
				mu.Lock()
				lastErr = event.Err
				mu.Unlock()
				generator.Send(event)
				break
			}

			if event.Action != nil {
				if event.Action.Exit {
					exit = true
					generator.Send(event)
					break
				}

				if event.Action.Interrupted != nil {
					mu.Lock()
					if interruptInfos == nil {
						interruptInfos = make(map[string]*InterruptInfo)
					}
					interruptInfos[fa.Name(ctx)] = event.Action.Interrupted
					mu.Unlock()
					break
				}

				if event.Action.TransferToAgent != nil {
					mu.Lock()
					if transferTargets == nil {
						transferTargets = make(map[string][]string)
					}
					transferTargets[fa.Name(ctx)] = append(transferTargets[fa.Name(ctx)], event.Action.TransferToAgent.DestAgentName)
					mu.Unlock()
				}
			} else {
				generator.Send(event)
			}
		}
	}

	for i := 1; i < len(c.Agents); i++ {
		a := c.Agents[i]
		fa := toFlowAgent(ctx, a)
		wg.Add(1)

		go r(fa)
	}

	wg.Add(1)
	fa := toFlowAgent(ctx, c.Agents[0])
	r(fa)

	wg.Wait()

	if lastErr != nil || exit {
		return iterator
	}

	var oneTarget string
	for _, targets := range transferTargets {
		if len(targets) > 0 {
			generator.Send(&AgentEvent{
				Err: fmt.Errorf("concurrent wrapper must transfer to only one agent, but found %v", targets)})
			return iterator
		}

		if len(oneTarget) > 0 && oneTarget != targets[0] {
			generator.Send(&AgentEvent{
				Err: fmt.Errorf("concurrent wrapper must transfer to the same agent, but found %s and %s", oneTarget, targets[0])})
			return iterator
		}

		oneTarget = targets[0]
	}

	if len(interruptInfos) > 0 {
		replaceInterruptRunCtx(ctx, runCtx)
		generator.Send(&AgentEvent{
			AgentName: c.Name(ctx),
			RunPath:   runCtx.RunPath,
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &concurrentInterruptInfo{
						InterruptInfos:  interruptInfos,
						TransferTargets: transferTargets,
					},
				},
			},
		})
		return iterator
	}

	if len(oneTarget) == 0 { // this concurrent wrapper just naturally stops
		return iterator
	}

	aMsg, tMsg := GenTransferMessages(ctx, oneTarget)
	aEvent := EventFromMessage(aMsg, nil, schema.Assistant, "")
	generator.Send(aEvent)
	tEvent := EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
	tEvent.Action = &AgentAction{
		TransferToAgent: &TransferToAgentAction{
			DestAgentName: oneTarget,
		},
	}
	generator.Send(tEvent)

	return iterator
}

func (c *ConcurrentWrapper) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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

	var (
		exit, hasErr    bool
		wg              = sync.WaitGroup{}
		transferTargets = intInfo.TransferTargets
		mu              = sync.Mutex{}
		runCtx          = getRunCtx(ctx)
		interruptInfos  map[string]*InterruptInfo
	)

	r := func(subRunCtx *runContext, fa *flowAgent) {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			}

			hasErr = true

			wg.Done()
		}()

		subRunCtx.RunPath = append(subRunCtx.RunPath, ExecutionStep{
			Single: generic.PtrOf(fa.Name(ctx)),
		})
		subCtx := setRunCtx(ctx, subRunCtx)
		subResumeInfo := &ResumeInfo{
			EnableStreaming: info.EnableStreaming,
			InterruptInfo:   intInfo.InterruptInfos[fa.Name(ctx)],
		}

		aIter := fa.Resume(subCtx, subResumeInfo, filterOptions(fa.Name(ctx), opts)...)

		for {
			event, ok := aIter.Next()
			if !ok {
				break
			}

			if event.Err != nil {
				hasErr = true
				generator.Send(event)
				break
			}

			if event.Action != nil {
				if event.Action.Exit {
					exit = true
					generator.Send(event)
					break
				}

				if event.Action.Interrupted != nil {
					mu.Lock()
					if interruptInfos == nil {
						interruptInfos = make(map[string]*InterruptInfo)
					}
					interruptInfos[fa.Name(ctx)] = event.Action.Interrupted
					mu.Unlock()
					break
				}

				if event.Action.TransferToAgent != nil {
					mu.Lock()
					if transferTargets == nil {
						transferTargets = make(map[string][]string)
					}
					transferTargets[fa.Name(ctx)] = append(transferTargets[fa.Name(ctx)], event.Action.TransferToAgent.DestAgentName)
					mu.Unlock()
				}
			} else {
				generator.Send(event)
			}
		}
	}

	var toResume []Agent
	for _, a := range c.Agents {
		name := a.Name(ctx)
		if _, ok := intInfo.InterruptInfos[name]; ok {
			toResume = append(toResume, a)
		}
	}

	for i := 1; i < len(toResume); i++ {
		a := c.Agents[i]
		fa := toFlowAgent(ctx, a)
		subRunCtx := runCtx.deepCopy()
		wg.Add(1)

		go func() {
			r(subRunCtx, fa)
		}()
	}

	fa := toFlowAgent(ctx, toResume[0])
	subRunCtx := runCtx.deepCopy()
	r(subRunCtx, fa)

	wg.Wait()

	if hasErr || exit {
		return iterator
	}

	var oneTarget string
	for _, targets := range transferTargets {
		if len(targets) > 0 {
			generator.Send(&AgentEvent{
				Err: fmt.Errorf("concurrent wrapper must transfer to only one agent, but found %v", targets)})
			return iterator
		}

		if len(oneTarget) > 0 && oneTarget != targets[0] {
			generator.Send(&AgentEvent{
				Err: fmt.Errorf("concurrent wrapper must transfer to the same agent, but found %s and %s", oneTarget, targets[0])})
			return iterator
		}

		oneTarget = targets[0]
	}

	if len(interruptInfos) > 0 {
		concurrentCtx := runCtx.deepCopy()
		names := make([]string, len(c.Agents))
		for i, a := range c.Agents {
			names[i] = a.Name(ctx)
		}
		concurrentCtx.RunPath = append(concurrentCtx.RunPath, ExecutionStep{
			Concurrent: names,
		})
		replaceInterruptRunCtx(ctx, concurrentCtx)
		generator.Send(&AgentEvent{
			AgentName: c.Name(ctx),
			RunPath:   concurrentCtx.RunPath,
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &concurrentInterruptInfo{
						InterruptInfos:  interruptInfos,
						TransferTargets: transferTargets,
					},
				},
			},
		})
		return iterator
	}

	if len(oneTarget) == 0 {
		generator.Send(&AgentEvent{
			Err: fmt.Errorf("concurrent wrapper must transfer to one agent if no interrupt, no exit and no error, " +
				"but found none"),
		})
		return iterator
	}

	aMsg, tMsg := GenTransferMessages(ctx, oneTarget)
	aEvent := EventFromMessage(aMsg, nil, schema.Assistant, "")
	generator.Send(aEvent)
	tEvent := EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
	tEvent.Action = &AgentAction{
		TransferToAgent: &TransferToAgentAction{
			DestAgentName: oneTarget,
		},
	}
	generator.Send(tEvent)

	return iterator
}
