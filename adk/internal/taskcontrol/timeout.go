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

// Package taskcontrol carries process-local controls scoped to one task execution.
package taskcontrol

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed reports that the task execution no longer accepts timeout requests.
var ErrClosed = errors.New("taskcontrol: timeout controller closed")

type controllerKey struct{}

// TimeoutRequest asks the bound execution to fail with Reason.
type TimeoutRequest struct {
	Reason string
	result chan error
}

// Complete acknowledges that the bound execution accepted the request.
func (r TimeoutRequest) Complete(err error) {
	select {
	case r.result <- err:
	default:
	}
}

// TimeoutController carries timeout requests for one execution.
type TimeoutController struct {
	requests  chan TimeoutRequest
	done      chan struct{}
	closeOnce sync.Once
}

// WithTimeoutController attaches a new execution-scoped timeout controller.
func WithTimeoutController(ctx context.Context) (context.Context, *TimeoutController) {
	controller := &TimeoutController{
		requests: make(chan TimeoutRequest, 1),
		done:     make(chan struct{}),
	}
	return context.WithValue(ctx, controllerKey{}, controller), controller
}

// FromContext returns the timeout controller attached to ctx.
func FromContext(ctx context.Context) *TimeoutController {
	if ctx == nil {
		return nil
	}
	controller, _ := ctx.Value(controllerKey{}).(*TimeoutController)
	return controller
}

// RequestTimeout synchronously requests deterministic timeout failure.
func (c *TimeoutController) RequestTimeout(ctx context.Context, reason string) error {
	if reason == "" {
		return errors.New("taskcontrol: timeout reason is required")
	}
	if c == nil {
		return ErrClosed
	}
	request := TimeoutRequest{Reason: reason, result: make(chan error, 1)}
	select {
	case c.requests <- request:
	case <-c.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.result:
		return err
	case <-c.done:
		select {
		case err := <-request.result:
			return err
		default:
			return ErrClosed
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Requests returns timeout requests awaiting execution acknowledgement.
func (c *TimeoutController) Requests() <-chan TimeoutRequest {
	if c == nil {
		return nil
	}
	return c.requests
}

// Done closes when the bound execution no longer accepts timeout requests.
func (c *TimeoutController) Done() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}

// Close rejects pending and future timeout requests.
func (c *TimeoutController) Close() {
	if c == nil {
		return
	}
	c.closeOnce.Do(func() { close(c.done) })
}
