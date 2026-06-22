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

func (w *typedTimeoutModelWrapper[M]) Stream(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	if !isConfigActive(w.config) {
		return w.inner.Stream(ctx, input, opts...)
	}

	started := time.Now()
	streamCtx, cancel := newStreamOpenCancelContext(ctx)
	timeoutState := newModelStreamTimeoutState[M](w.config, started, cancel)
	defer func() {
		if timeoutState.reader == nil {
			timeoutState.stop()
			cancel()
		}
	}()

	openTimeout, openPhase, hasOpenTimeout := minPositiveTimeout(w.config.CallTimeout, w.config.TotalTimeout)
	var openTimer *time.Timer
	if hasOpenTimeout {
		openTimer = time.AfterFunc(openTimeout, func() {
			timeoutState.timeout(openPhase, openTimeout)
		})
	}

	reader, err := w.inner.Stream(streamCtx, input, opts...)
	if openTimer != nil && !openTimer.Stop() {
		if reader != nil {
			reader.Close()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if timeoutErr := timeoutState.err(); timeoutErr != nil {
			return nil, timeoutErr
		}
	}
	if err != nil {
		if ctx.Err() == nil {
			if timeoutErr := timeoutState.err(); timeoutErr != nil {
				return nil, timeoutErr
			}
		}
		return nil, err
	}
	if reader == nil {
		return nil, errors.New("model Stream returned nil reader without error")
	}
	if !w.hasStreamBodyTimeout() {
		timeoutState.reader = schema.StreamReaderWithConvert(reader, func(msg M) (M, error) {
			return msg, nil
		}, schema.WithOnEOF(func() (any, error) {
			timeoutState.stop()
			cancel()
			return nil, io.EOF
		}), schema.WithErrWrapper(func(streamErr error) error {
			timeoutState.stop()
			cancel()
			return streamErr
		}))
		return timeoutState.reader, nil
	}
	timeoutState.reader = w.wrapStreamBody(ctx, streamCtx, cancel, reader, started, timeoutState)
	return timeoutState.reader, nil
}

func newStreamOpenCancelContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(ctx)
}

func (w *typedTimeoutModelWrapper[M]) hasStreamBodyTimeout() bool {
	return w.config.FirstChunkTimeout > 0 || w.config.StreamIdleTimeout > 0 || w.config.TotalTimeout > 0
}

type modelStreamTimeoutState[M adk.MessageType] struct {
	config  *Config
	started time.Time
	cancel  context.CancelFunc
	reader  *schema.StreamReader[M]

	mu              sync.Mutex
	inactivityTimer *time.Timer
	totalTimer      *time.Timer
	chunks          int
	terminal        bool
	errValue        *Error
}

func newModelStreamTimeoutState[M adk.MessageType](config *Config, started time.Time, cancel context.CancelFunc) *modelStreamTimeoutState[M] {
	return &modelStreamTimeoutState[M]{config: config, started: started, cancel: cancel}
}

func (s *modelStreamTimeoutState[M]) timeout(phase Phase, timeout time.Duration) {
	s.mu.Lock()
	if !s.terminal && s.errValue == nil {
		s.errValue = modelTimeoutError(phase, timeout, s.started, s.chunks)
	}
	s.mu.Unlock()
	s.cancel()
}

func (s *modelStreamTimeoutState[M]) stop() {
	s.mu.Lock()
	if s.inactivityTimer != nil {
		s.inactivityTimer.Stop()
		s.inactivityTimer = nil
	}
	if s.totalTimer != nil {
		s.totalTimer.Stop()
		s.totalTimer = nil
	}
	s.terminal = true
	s.mu.Unlock()
}

func (s *modelStreamTimeoutState[M]) err() *Error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.errValue
}

func (s *modelStreamTimeoutState[M]) afterChunk() {
	s.mu.Lock()
	if s.terminal || s.errValue != nil {
		s.mu.Unlock()
		return
	}
	s.chunks++
	s.resetInactivityLocked(s.config.StreamIdleTimeout, PhaseStreamIdle)
	s.mu.Unlock()
}

func (s *modelStreamTimeoutState[M]) startBodyTimers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal || s.errValue != nil {
		return
	}
	if s.config.TotalTimeout > 0 {
		remaining := time.Until(s.started.Add(s.config.TotalTimeout))
		if remaining < 0 {
			remaining = 0
		}
		s.totalTimer = time.AfterFunc(remaining, func() {
			s.timeout(PhaseTotal, s.config.TotalTimeout)
		})
	}
	s.resetInactivityLocked(s.config.FirstChunkTimeout, PhaseFirstChunk)
}

func (s *modelStreamTimeoutState[M]) resetInactivityLocked(timeout time.Duration, phase Phase) {
	if s.inactivityTimer != nil {
		s.inactivityTimer.Stop()
		s.inactivityTimer = nil
	}
	if timeout <= 0 {
		return
	}
	s.inactivityTimer = time.AfterFunc(timeout, func() {
		s.timeout(phase, timeout)
	})
}

func (w *typedTimeoutModelWrapper[M]) wrapStreamBody(
	ctx context.Context,
	streamCtx context.Context,
	cancel context.CancelFunc,
	upstream *schema.StreamReader[M],
	_ time.Time,
	timeoutState *modelStreamTimeoutState[M],
) *schema.StreamReader[M] {
	timeoutState.startBodyTimers()
	return schema.StreamReaderWithConvert(upstream,
		func(msg M) (M, error) {
			timeoutState.afterChunk()
			return msg, nil
		},
		schema.WithOnEOF(func() (any, error) {
			timeoutState.stop()
			cancel()
			return nil, io.EOF
		}),
		schema.WithErrWrapper(func(streamErr error) error {
			timeoutState.stop()
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if timeoutErr := timeoutState.err(); timeoutErr != nil {
				return timeoutErr
			}
			if streamCtx.Err() != nil {
				return streamCtx.Err()
			}
			return streamErr
		}),
	)
}
