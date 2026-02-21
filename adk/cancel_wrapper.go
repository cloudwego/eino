package adk

import (
	"context"
	"io"
	"runtime/debug"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type cancelableChatModel struct {
	inner model.BaseChatModel
	cs    *cancelSig
}

func wrapModelForCancelable(m model.BaseChatModel, cs *cancelSig) *cancelableChatModel {
	return &cancelableChatModel{inner: m, cs: cs}
}

func (c *cancelableChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if cfg := checkCancelSig(c.cs); cfg != nil && cfg.Mode == CancelImmediate {
		return nil, compose.Interrupt(ctx, "cancelled externally")
	}
	type resultPair struct {
		res *schema.Message
		err error
	}
	resultCh := make(chan *resultPair, 1)
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				resultCh <- &resultPair{nil, e}
			}
		}()

		res, err := c.inner.Generate(ctx, input, opts...)
		resultCh <- &resultPair{res: res, err: err}
	}()

	var timeCh <-chan time.Time
	select {
	case <-c.cs.done:
		cfg := c.cs.config.Load().(*cancelConfig)
		if cfg.Mode == CancelImmediate {
			if cfg.Timeout == nil {
				return nil, compose.Interrupt(ctx, "cancelled externally")
			}
			timeCh = time.After(*cfg.Timeout)
		}
	case res := <-resultCh:
		return res.res, res.err
	}
	select {
	case <-timeCh:
		return nil, compose.Interrupt(ctx, "cancelled externally")
	case res := <-resultCh:
		return res.res, res.err
	}
}

func (c *cancelableChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if cfg := checkCancelSig(c.cs); cfg != nil && cfg.Mode == CancelImmediate {
		return nil, compose.Interrupt(ctx, "cancelled externally")
	}

	type resultPair struct {
		stream *schema.StreamReader[*schema.Message]
		err    error
	}
	resultCh := make(chan *resultPair, 1)
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				resultCh <- &resultPair{nil, e}
			}
		}()

		stream, err := c.inner.Stream(ctx, input, opts...)
		if err != nil {
			resultCh <- &resultPair{nil, err}
			return
		}
		copies := stream.Copy(2)
		checkCopy := copies[0]
		returnCopy := copies[1]

		_ = consumeStreamForError(checkCopy)

		resultCh <- &resultPair{stream: returnCopy, err: nil}
	}()

	var timeCh <-chan time.Time
	select {
	case <-c.cs.done:
		cfg := c.cs.config.Load().(*cancelConfig)
		if cfg.Mode == CancelImmediate {
			if cfg.Timeout == nil {
				return nil, compose.Interrupt(ctx, "cancelled externally")
			}
			timeCh = time.After(*cfg.Timeout)
		}
	case res := <-resultCh:
		return res.stream, res.err
	}
	select {
	case <-timeCh:
		return nil, compose.Interrupt(ctx, "cancelled externally")
	case res := <-resultCh:
		return res.stream, res.err
	}
}

func cancelableTool(cs *cancelSig) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(endpoint compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
					return nil, compose.Interrupt(ctx, "cancelled externally")
				}

				type resultPair struct {
					output *compose.ToolOutput
					err    error
				}
				resultCh := make(chan *resultPair, 1)
				go func() {
					defer func() {
						panicErr := recover()
						if panicErr != nil {
							e := safe.NewPanicErr(panicErr, debug.Stack())
							resultCh <- &resultPair{nil, e}
						}
					}()

					output, err := endpoint(ctx, input)
					resultCh <- &resultPair{output: output, err: err}
				}()

				var timeCh <-chan time.Time
				select {
				case <-cs.done:
					cfg := cs.config.Load().(*cancelConfig)
					if cfg.Mode == CancelImmediate {
						if cfg.Timeout == nil {
							return nil, compose.Interrupt(ctx, "cancelled externally")
						}
						timeCh = time.After(*cfg.Timeout)
					}
				case res := <-resultCh:
					return res.output, res.err
				}
				select {
				case <-timeCh:
					return nil, compose.Interrupt(ctx, "cancelled externally")
				case res := <-resultCh:
					return res.output, res.err
				}
			}
		},
		Streamable: func(endpoint compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
					return nil, compose.Interrupt(ctx, "cancelled externally")
				}

				type resultPair struct {
					stream *schema.StreamReader[string]
					err    error
				}
				resultCh := make(chan *resultPair, 1)
				go func() {
					defer func() {
						panicErr := recover()
						if panicErr != nil {
							e := safe.NewPanicErr(panicErr, debug.Stack())
							resultCh <- &resultPair{nil, e}
						}
					}()

					output, err := endpoint(ctx, input)
					if err != nil {
						resultCh <- &resultPair{nil, err}
						return
					}

					copies := output.Result.Copy(2)
					checkCopy := copies[0]
					returnCopy := copies[1]

					_ = consumeStreamForErrorString(checkCopy)

					resultCh <- &resultPair{stream: returnCopy, err: nil}
				}()

				var timeCh <-chan time.Time
				select {
				case <-cs.done:
					cfg := cs.config.Load().(*cancelConfig)
					if cfg.Mode == CancelImmediate {
						if cfg.Timeout == nil {
							return nil, compose.Interrupt(ctx, "cancelled externally")
						}
						timeCh = time.After(*cfg.Timeout)
					}
				case res := <-resultCh:
					if res.err != nil {
						return nil, res.err
					}
					return &compose.StreamToolOutput{Result: res.stream}, nil
				}
				select {
				case <-timeCh:
					return nil, compose.Interrupt(ctx, "cancelled externally")
				case res := <-resultCh:
					if res.err != nil {
						return nil, res.err
					}
					return &compose.StreamToolOutput{Result: res.stream}, nil
				}
			}
		},
		EnhancedInvokable: func(endpoint compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
				if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
					return nil, compose.Interrupt(ctx, "cancelled externally")
				}

				type resultPair struct {
					output *compose.EnhancedInvokableToolOutput
					err    error
				}
				resultCh := make(chan *resultPair, 1)
				go func() {
					defer func() {
						panicErr := recover()
						if panicErr != nil {
							e := safe.NewPanicErr(panicErr, debug.Stack())
							resultCh <- &resultPair{nil, e}
						}
					}()

					output, err := endpoint(ctx, input)
					resultCh <- &resultPair{output: output, err: err}
				}()

				var timeCh <-chan time.Time
				select {
				case <-cs.done:
					cfg := cs.config.Load().(*cancelConfig)
					if cfg.Mode == CancelImmediate {
						if cfg.Timeout == nil {
							return nil, compose.Interrupt(ctx, "cancelled externally")
						}
						timeCh = time.After(*cfg.Timeout)
					}
				case res := <-resultCh:
					return res.output, res.err
				}
				select {
				case <-timeCh:
					return nil, compose.Interrupt(ctx, "cancelled externally")
				case res := <-resultCh:
					return res.output, res.err
				}
			}
		},
		EnhancedStreamable: func(endpoint compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
				if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
					return nil, compose.Interrupt(ctx, "cancelled externally")
				}

				type resultPair struct {
					stream *schema.StreamReader[*schema.ToolResult]
					err    error
				}
				resultCh := make(chan *resultPair, 1)
				go func() {
					defer func() {
						panicErr := recover()
						if panicErr != nil {
							e := safe.NewPanicErr(panicErr, debug.Stack())
							resultCh <- &resultPair{nil, e}
						}
					}()

					output, err := endpoint(ctx, input)
					if err != nil {
						resultCh <- &resultPair{nil, err}
						return
					}

					copies := output.Result.Copy(2)
					checkCopy := copies[0]
					returnCopy := copies[1]

					_ = consumeStreamForErrorToolResult(checkCopy)

					resultCh <- &resultPair{stream: returnCopy, err: nil}
				}()

				var timeCh <-chan time.Time
				select {
				case <-cs.done:
					cfg := cs.config.Load().(*cancelConfig)
					if cfg.Mode == CancelImmediate {
						if cfg.Timeout == nil {
							return nil, compose.Interrupt(ctx, "cancelled externally")
						}
						timeCh = time.After(*cfg.Timeout)
					}
				case res := <-resultCh:
					if res.err != nil {
						return nil, res.err
					}
					return &compose.EnhancedStreamableToolOutput{Result: res.stream}, nil
				}
				select {
				case <-timeCh:
					return nil, compose.Interrupt(ctx, "cancelled externally")
				case res := <-resultCh:
					if res.err != nil {
						return nil, res.err
					}
					return &compose.EnhancedStreamableToolOutput{Result: res.stream}, nil
				}
			}
		},
	}
}

func consumeStreamForErrorString(stream *schema.StreamReader[string]) error {
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func consumeStreamForErrorToolResult(stream *schema.StreamReader[*schema.ToolResult]) error {
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
