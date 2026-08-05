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

package sessionnotify

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
)

// TurnLoopTarget is a deployment-owned session loop and its process-lifetime
// context. The context must not be derived from a notification dispatch
// request; the deployment owns cancellation after all users release the target.
type TurnLoopTarget[T any, M adk.MessageType] struct {
	Loop       *adk.TurnLoop[T, M]
	RunContext context.Context
}

// TurnLoopActivator bridges durable session wake requests to the ADK TurnLoop.
// The loop's GenInput implementation remains responsible for reading and
// acknowledging durable inbox items. Resolve and WakeItem may be called
// concurrently, must not panic, and must tolerate repeated activation for the
// same pending notification. Resolve lends the target for one RequestTurn call;
// it must remain valid through Loop.Run.
type TurnLoopActivator[T any, M adk.MessageType] struct {
	Resolve  func(context.Context, string) (*TurnLoopTarget[T, M], error)
	WakeItem func(*backgroundtask.SessionActivationRequest) (T, error)
}

func (a *TurnLoopActivator[T, M]) RequestTurn(
	ctx context.Context,
	req *backgroundtask.SessionActivationRequest,
) (*backgroundtask.SessionActivationResult, error) {
	if req == nil || req.SessionID == "" {
		return nil, errors.New("sessionnotify: session id is required")
	}
	if a == nil || a.Resolve == nil || a.WakeItem == nil {
		return nil, errors.New("sessionnotify: turn loop resolver and wake item encoder are required")
	}
	target, err := a.Resolve(ctx, req.SessionID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.Loop == nil || target.RunContext == nil {
		return nil, errors.New("sessionnotify: resolved turn loop target is incomplete")
	}
	item, err := a.WakeItem(req)
	if err != nil {
		return nil, err
	}
	accepted, _ := target.Loop.Push(item)
	if !accepted {
		return nil, errors.New("sessionnotify: resolved turn loop is stopped")
	}
	target.Loop.Run(target.RunContext)
	return &backgroundtask.SessionActivationResult{
		Disposition: backgroundtask.SessionActivationQueued,
	}, nil
}
