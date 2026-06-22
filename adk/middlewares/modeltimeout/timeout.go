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

package modeltimeout

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Phase identifies which part of a model call exceeded its budget.
type Phase string

const (
	// PhaseCall means Generate or Stream opening exceeded its budget.
	PhaseCall Phase = "call"
	// PhaseFirstChunk means no stream chunk arrived before the first-chunk budget.
	PhaseFirstChunk Phase = "first_chunk"
	// PhaseStreamIdle means the stream exceeded its inter-chunk idle budget.
	PhaseStreamIdle Phase = "stream_idle"
	// PhaseTotal means the whole Generate call or Stream lifecycle exceeded its budget.
	PhaseTotal Phase = "total"
)

// ErrModelTimeout is the sentinel matched by Error.
var ErrModelTimeout = errors.New("model timeout")

// Config configures opt-in timeout enforcement for ChatModel calls.
//
// Timeout errors are surfaced as *Error and can be handled by
// ModelRetryConfig.ShouldRetry/IsRetryAble and ModelFailoverConfig.ShouldFailover.
// If nil or all durations are <= 0, no timeout wrapper is installed.
//
// Timeouts are per model attempt because the timeout wrapper sits inside retry
// and failover. Providers must respect context cancellation for Generate/Stream
// opening and context cancellation or StreamReader.Close for stream-body cleanup.
type Config struct {
	// CallTimeout bounds Generate and Stream until Stream returns a reader.
	// For Generate this is effectively the non-streaming model call timeout.
	// For Stream this is the request-open/header/reader-acquisition timeout.
	CallTimeout time.Duration

	// FirstChunkTimeout bounds the time from Stream returning a reader to the
	// first successful chunk.
	FirstChunkTimeout time.Duration

	// StreamIdleTimeout bounds the gap between successful stream chunks after
	// the first chunk.
	StreamIdleTimeout time.Duration

	// TotalTimeout bounds the whole Generate call or whole Stream lifecycle.
	// It is per model attempt when retry/failover are configured.
	TotalTimeout time.Duration
}

// Error reports a model timeout without prescribing retry policy.
type Error struct {
	Phase          Phase
	Timeout        time.Duration
	Elapsed        time.Duration
	ChunksReceived int
}

func (e *Error) Error() string {
	if e == nil {
		return ErrModelTimeout.Error()
	}
	return fmt.Sprintf("model timeout: phase=%s timeout=%s elapsed=%s chunks_received=%d",
		e.Phase, e.Timeout, e.Elapsed, e.ChunksReceived)
}

func (e *Error) Is(target error) bool {
	return target == ErrModelTimeout
}

// IsModelTimeoutBeforeOutput reports whether the timeout happened before any
// stream output reached downstream consumers.
func (e *Error) IsModelTimeoutBeforeOutput() bool {
	return e != nil && e.ChunksReceived == 0
}

// ModelTimeoutSpanMeta exposes timeout details to packages that should not
// import this middleware package directly.
func (e *Error) ModelTimeoutSpanMeta() (phase string, timeout time.Duration, elapsed time.Duration, chunksReceived int) {
	if e == nil {
		return "", 0, 0, 0
	}
	return string(e.Phase), e.Timeout, e.Elapsed, e.ChunksReceived
}

// AsModelTimeout extracts a Error from err.
func AsModelTimeout(err error) (*Error, bool) {
	var timeoutErr *Error
	if errors.As(err, &timeoutErr) {
		return timeoutErr, true
	}
	return nil, false
}

// IsModelTimeoutBeforeOutput reports whether err is a timeout that happened
// before any stream output reached downstream consumers.
func IsModelTimeoutBeforeOutput(err error) bool {
	timeoutErr, ok := AsModelTimeout(err)
	return ok && timeoutErr.ChunksReceived == 0
}

func init() {
	schema.RegisterName[*Error]("_eino_adk_model_timeout_error")
}

type typedTimeoutModelWrapper[M adk.MessageType] struct {
	inner  model.BaseModel[M]
	config *Config
}

// NewTypedTimeoutModelWrapper wraps a model with timeout enforcement.
//
// Prefer configuring this through adk/middlewares/modeltimeout so timeout
// behavior composes with other ChatModelAgent middlewares.
func NewTypedTimeoutModelWrapper[M adk.MessageType](inner model.BaseModel[M], config *Config) model.BaseModel[M] {
	return &typedTimeoutModelWrapper[M]{inner: inner, config: config}
}

func newTypedTimeoutModelWrapper[M adk.MessageType](inner model.BaseModel[M], config *Config) model.BaseModel[M] {
	return NewTypedTimeoutModelWrapper(inner, config)
}

// IsConfigActive reports whether config enables any timeout.
func IsConfigActive(config *Config) bool {
	return config != nil && (config.CallTimeout > 0 ||
		config.FirstChunkTimeout > 0 ||
		config.StreamIdleTimeout > 0 ||
		config.TotalTimeout > 0)
}

func isConfigActive(config *Config) bool {
	return IsConfigActive(config)
}

func minPositiveTimeout(callTimeout, totalTimeout time.Duration) (time.Duration, Phase, bool) {
	switch {
	case callTimeout > 0 && totalTimeout > 0:
		if totalTimeout <= callTimeout {
			return totalTimeout, PhaseTotal, true
		}
		return callTimeout, PhaseCall, true
	case callTimeout > 0:
		return callTimeout, PhaseCall, true
	case totalTimeout > 0:
		return totalTimeout, PhaseTotal, true
	default:
		return 0, "", false
	}
}

func modelTimeoutError(phase Phase, timeout time.Duration, started time.Time, chunks int) *Error {
	return &Error{
		Phase:          phase,
		Timeout:        timeout,
		Elapsed:        time.Since(started),
		ChunksReceived: chunks,
	}
}

type timeoutGenerateResult[M adk.MessageType] struct {
	msg M
	err error
}

func (w *typedTimeoutModelWrapper[M]) Generate(ctx context.Context, input []M, opts ...model.Option) (M, error) {
	timeout, phase, ok := minPositiveTimeout(w.config.CallTimeout, w.config.TotalTimeout)
	if !ok {
		return w.inner.Generate(ctx, input, opts...)
	}

	started := time.Now()
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resultCh := make(chan timeoutGenerateResult[M], 1)
	go func() {
		msg, err := w.inner.Generate(timeoutCtx, input, opts...)
		resultCh <- timeoutGenerateResult[M]{msg: msg, err: err}
	}()

	select {
	case result := <-resultCh:
		if ctx.Err() == nil && errors.Is(result.err, context.DeadlineExceeded) && errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
			var zero M
			return zero, modelTimeoutError(phase, timeout, started, 0)
		}
		return result.msg, result.err
	case <-ctx.Done():
		var zero M
		cancel()
		return zero, ctx.Err()
	case <-timeoutCtx.Done():
		var zero M
		cancel()
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, modelTimeoutError(phase, timeout, started, 0)
	}
}

type timeoutStreamOpenResult[M adk.MessageType] struct {
	reader *schema.StreamReader[M]
	err    error
}

func (w *typedTimeoutModelWrapper[M]) Stream(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	if !isConfigActive(w.config) {
		return w.inner.Stream(ctx, input, opts...)
	}

	started := time.Now()
	bodyTimeoutActive := w.hasStreamBodyTimeout()
	streamCtx := ctx
	cancel := func() {}
	if bodyTimeoutActive && w.config.TotalTimeout > 0 {
		streamCtx, cancel = newStreamOpenTimeoutContext(ctx, w.config.TotalTimeout)
	} else if bodyTimeoutActive || w.config.CallTimeout > 0 {
		streamCtx, cancel = newStreamOpenCancelContext(ctx)
	}

	resultCh := make(chan timeoutStreamOpenResult[M], 1)
	done := make(chan struct{})
	accepted := make(chan struct{})
	go func() {
		reader, err := w.inner.Stream(streamCtx, input, opts...)
		result := timeoutStreamOpenResult[M]{reader: reader, err: err}
		select {
		case <-done:
			if reader != nil {
				reader.Close()
			}
		case resultCh <- result:
			select {
			case <-accepted:
			case <-done:
				if reader != nil {
					reader.Close()
				}
			}
		}
	}()

	openTimeout, openPhase, hasOpenTimeout := minPositiveTimeout(w.config.CallTimeout, w.config.TotalTimeout)
	var openTimer *time.Timer
	var openTimeoutCh <-chan time.Time
	if hasOpenTimeout {
		openTimer = time.NewTimer(openTimeout)
		openTimeoutCh = openTimer.C
		defer openTimer.Stop()
	}

	var result timeoutStreamOpenResult[M]
	select {
	case result = <-resultCh:
		close(accepted)
		if ctx.Err() == nil && hasOpenTimeout && result.err != nil &&
			streamCtx.Err() != nil && time.Since(started) >= openTimeout {
			cancel()
			return nil, modelTimeoutError(openPhase, openTimeout, started, 0)
		}
		if result.err != nil {
			cancel()
			return nil, result.err
		}
	case <-ctx.Done():
		close(done)
		cancel()
		return nil, ctx.Err()
	case <-openTimeoutCh:
		close(done)
		cancel()
		return nil, modelTimeoutError(openPhase, openTimeout, started, 0)
	case <-streamCtx.Done():
		close(done)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, modelTimeoutError(PhaseTotal, w.config.TotalTimeout, started, 0)
	}

	if result.reader == nil {
		cancel()
		return nil, errors.New("model Stream returned nil reader without error")
	}
	if !bodyTimeoutActive {
		return result.reader, nil
	}
	return w.wrapStreamBody(ctx, streamCtx, cancel, result.reader, started), nil
}

func newStreamOpenCancelContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

func newStreamOpenTimeoutContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

func (w *typedTimeoutModelWrapper[M]) hasStreamBodyTimeout() bool {
	return w.config.FirstChunkTimeout > 0 || w.config.StreamIdleTimeout > 0 || w.config.TotalTimeout > 0
}

type timeoutStreamWriter[M adk.MessageType] struct {
	writer *schema.StreamWriter[M]
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
}

func newTimeoutStreamWriter[M adk.MessageType](writer *schema.StreamWriter[M]) *timeoutStreamWriter[M] {
	return &timeoutStreamWriter[M]{
		writer: writer,
		done:   make(chan struct{}),
	}
}

func (w *timeoutStreamWriter[M]) send(msg M, err error) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return true
	}
	return w.writer.Send(msg, err)
}

func (w *timeoutStreamWriter[M]) close() {
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.writer.Close()
		w.mu.Unlock()
		close(w.done)
	})
}

func (w *typedTimeoutModelWrapper[M]) wrapStreamBody(
	ctx context.Context,
	streamCtx context.Context,
	cancel context.CancelFunc,
	upstream *schema.StreamReader[M],
	started time.Time,
) *schema.StreamReader[M] {
	reader, writer := schema.Pipe[M](1)
	terminal := newTimeoutStreamWriter(writer)
	var chunks int32
	activity := make(chan struct{}, 1)
	var finishOnce sync.Once

	finish := func(err error) {
		finishOnce.Do(func() {
			if err != nil {
				var zero M
				terminal.send(zero, err)
			}
			terminal.close()
			upstream.Close()
			cancel()
		})
	}

	go func() {
		for {
			msg, err := upstream.Recv()
			if err == io.EOF {
				finish(nil)
				return
			}
			if err != nil {
				if ctx.Err() != nil {
					finish(ctx.Err())
					return
				}
				if streamCtx.Err() != nil && w.config.TotalTimeout > 0 {
					finish(modelTimeoutError(PhaseTotal, w.config.TotalTimeout, started, int(atomic.LoadInt32(&chunks))))
					return
				}
				finish(err)
				return
			}
			if terminal.send(msg, nil) {
				finish(nil)
				return
			}
			atomic.AddInt32(&chunks, 1)
			select {
			case activity <- struct{}{}:
			default:
			}
		}
	}()

	go func() {
		firstReceived := false
		var inactivityTimer *time.Timer
		var inactivityCh <-chan time.Time
		resetInactivity := func(d time.Duration) {
			if inactivityTimer != nil {
				if !inactivityTimer.Stop() {
					select {
					case <-inactivityTimer.C:
					default:
					}
				}
			}
			if d > 0 {
				inactivityTimer = time.NewTimer(d)
				inactivityCh = inactivityTimer.C
			} else {
				inactivityCh = nil
			}
		}
		defer func() {
			if inactivityTimer != nil {
				inactivityTimer.Stop()
			}
		}()

		resetInactivity(w.config.FirstChunkTimeout)
		var totalTimer *time.Timer
		var totalCh <-chan time.Time
		if w.config.TotalTimeout > 0 {
			remaining := time.Until(started.Add(w.config.TotalTimeout))
			if remaining < 0 {
				remaining = 0
			}
			totalTimer = time.NewTimer(remaining)
			totalCh = totalTimer.C
			defer totalTimer.Stop()
		}

		for {
			select {
			case <-terminal.done:
				return
			case <-activity:
				if !firstReceived {
					firstReceived = true
				}
				resetInactivity(w.config.StreamIdleTimeout)
			case <-inactivityCh:
				phase := PhaseFirstChunk
				timeout := w.config.FirstChunkTimeout
				if firstReceived {
					phase = PhaseStreamIdle
					timeout = w.config.StreamIdleTimeout
				}
				finish(modelTimeoutError(phase, timeout, started, int(atomic.LoadInt32(&chunks))))
				return
			case <-totalCh:
				finish(modelTimeoutError(PhaseTotal, w.config.TotalTimeout, started, int(atomic.LoadInt32(&chunks))))
				return
			case <-streamCtx.Done():
				if ctx.Err() != nil {
					finish(ctx.Err())
					return
				}
				if w.config.TotalTimeout > 0 {
					finish(modelTimeoutError(PhaseTotal, w.config.TotalTimeout, started, int(atomic.LoadInt32(&chunks))))
					return
				}
				finish(streamCtx.Err())
				return
			}
		}
	}()

	return reader
}
