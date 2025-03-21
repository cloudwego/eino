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

package compose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
)

func TestChainBranch(t *testing.T) {
	cond := func(ctx context.Context, input string) (key string, err error) {
		switch input {
		case "one":
			return "one_key", nil
		case "two":
			return "two_key", nil
		case "three":
			return "three_key", nil
		default:
			return "", fmt.Errorf("invalid input= %s", input)
		}
	}

	t.Run("nested chain", func(t *testing.T) {
		inner := NewChain[string, string]()
		inner.AppendBranch(NewChainBranch(cond).
			AddLambda("one_key", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
				return in + in, nil
			})).
			AddLambda("two_key", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
				return in + in + in, nil
			})))
		inner.AppendParallel(NewParallel().
			AddLambda("one_key", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
				return in + in, nil
			})).
			AddLambda("two_key", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
				return in + in + in, nil
			})))

		outer := NewChain[string, string]()
		outer.AppendGraph(inner)
		_, err := outer.Compile(context.Background())
		assert.Error(t, err)
	})

	t.Run("bad param", func(t *testing.T) {
		c := NewChain[string, string]()
		c.AppendBranch(nil)
		assert.NotNil(t, c.err)

		c = NewChain[string, string]()
		c.AppendBranch(NewChainBranch[string](nil))
		assert.NotNil(t, c.err)

		c = NewChain[string, string]()
		c.AppendBranch(NewChainBranch(cond).AddChatTemplate("template", prompt.FromMessages(schema.FString, schema.SystemMessage("hello"))))
		assert.NotNil(t, c.err)

		c = NewChain[string, string]()
		c.AppendBranch(NewChainBranch(cond).AddChatTemplate("1", prompt.FromMessages(schema.FString)).AddChatTemplate("1", prompt.FromMessages(schema.FString)))
		assert.NotNil(t, c.err)
	})

	t.Run("different Node types in branch", func(t *testing.T) {
		c := NewChain[string, string]()
		c.AppendBranch(NewChainBranch(cond).
			AddChatTemplate("t", prompt.FromMessages(schema.FString)).
			AddGraph("c", NewChain[string, string]()))
		assert.NotNil(t, c.err)
	})

	t.Run("type mismatch", func(t *testing.T) {
		c := NewChain[int, string]()
		c.AppendBranch(NewChainBranch(cond).
			AddLambda("one_key", InvokableLambda(func(ctx context.Context, in int) (output string, err error) {
				return strconv.Itoa(in), nil
			})).
			AddLambda("two_key", InvokableLambda(func(ctx context.Context, in int) (output string, err error) {
				return strconv.Itoa(in), nil
			})))
		_, err := c.Compile(context.Background())
		assert.NotNil(t, err)
	})

	t.Run("invoke", func(t *testing.T) {
		c := NewChain[string, string]()
		c.AppendBranch(NewChainBranch(cond).
			AddLambda("one_key", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
				return in + in, nil
			})).
			AddLambda("two_key", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
				return in + in + in, nil
			})))
		c.AppendLambda(InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + in, nil
		}))
		assert.Nil(t, c.err)
		compiledChain, err := c.Compile(context.Background())
		assert.Nil(t, err)

		out, err := compiledChain.Invoke(context.Background(), "two")
		assert.Nil(t, err)
		assert.Equal(t, "twotwotwotwotwotwo", out)

		_, err = compiledChain.Invoke(context.Background(), "three")
		assert.NotNil(t, err)

		_, err = compiledChain.Invoke(context.Background(), "four")
		assert.NotNil(t, err)
	})

	t.Run("fake stream", func(t *testing.T) {
		c := NewChain[string, string]()
		c.AppendLambda(StreamableLambda(func(ctx context.Context, in string) (output *schema.StreamReader[string], err error) {
			sr, sw := schema.Pipe[string](utf8.RuneCountInString(in))

			go func() {
				for _, field := range strings.Fields(in) {
					sw.Send(field, nil)
				}
				sw.Close()
			}()

			return sr, nil
		}))
		c.AppendBranch(NewChainBranch[string](cond).AddLambda("one_key", CollectableLambda(func(ctx context.Context, in *schema.StreamReader[string]) (output string, err error) {
			defer in.Close()
			for {
				v, err := in.Recv()
				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					return "", err
				}

				output += v
			}

			return output + output, nil
		})).
			AddLambda("two_key", CollectableLambda(func(ctx context.Context, in *schema.StreamReader[string]) (output string, err error) {
				defer in.Close()
				for {
					v, err := in.Recv()
					if errors.Is(err, io.EOF) {
						break
					}

					if err != nil {
						return "", err
					}

					output += v
				}

				return output + output + output, nil
			})))

		assert.Nil(t, c.err)
		compiledChain, err := c.Compile(context.Background())
		assert.Nil(t, err)

		out, err := compiledChain.Invoke(context.Background(), "one")
		assert.Nil(t, err)
		assert.Equal(t, "oneone", out)
	})

	t.Run("real stream", func(t *testing.T) {
		streamCon := func(ctx context.Context, sr *schema.StreamReader[string]) (key string, err error) {
			msg, err := sr.Recv()
			if err != nil {
				return "", err
			}
			defer sr.Close()

			switch msg {
			case "one":
				return "one_key", nil
			case "two":
				return "two_key", nil
			case "three":
				return "three_key", nil
			default:
				return "", fmt.Errorf("invalid input= %s", msg)
			}
		}

		c := NewChain[string, string]()
		c.AppendLambda(StreamableLambda(func(ctx context.Context, in string) (output *schema.StreamReader[string], err error) {
			sr, sw := schema.Pipe[string](utf8.RuneCountInString(in))

			go func() {
				for _, field := range strings.Fields(in) {
					sw.Send(field, nil)
				}
				sw.Close()
			}()

			return sr, nil
		}))
		c.AppendBranch(NewStreamChainBranch(streamCon).AddLambda("one_key", CollectableLambda(func(ctx context.Context, in *schema.StreamReader[string]) (output string, err error) {
			defer in.Close()
			for {
				v, err := in.Recv()
				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					return "", err
				}

				output += v
			}

			return output + output, nil
		})).
			AddLambda("two_key", CollectableLambda(func(ctx context.Context, in *schema.StreamReader[string]) (output string, err error) {
				defer in.Close()
				for {
					v, err := in.Recv()
					if errors.Is(err, io.EOF) {
						break
					}

					if err != nil {
						return "", err
					}

					output += v
				}

				return output + output + output, nil
			})))

		assert.Nil(t, c.err)
		compiledChain, err := c.Compile(context.Background())
		assert.Nil(t, err)

		out, err := compiledChain.Stream(context.Background(), "one size fit all")
		assert.Nil(t, err)
		concat, err := concatStreamReader(out)
		assert.Nil(t, err)
		assert.Equal(t, "onesizefitallonesizefitall", concat)
	})
}

func TestChainMultiBranch(t *testing.T) {
	emptyLambda := InvokableLambda(func(ctx context.Context, input string) (output string, err error) { return input, nil })

	ctx := context.Background()
	r, err := NewChain[string, map[string]any]().
		AppendBranch(NewChainMultiBranch(func(ctx context.Context, in string) (endNode map[string]bool, err error) {
			return map[string]bool{"1": true, "2": true}, nil
		}).AddLambda("1", emptyLambda, WithOutputKey("1")).AddLambda("2", emptyLambda, WithOutputKey("2")).AddLambda("3", emptyLambda, WithOutputKey("3"))).
		Compile(ctx)
	assert.Nil(t, err)

	result, err := r.Invoke(ctx, "start")
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)

	streamResult, err := r.Stream(ctx, "start")
	assert.NoError(t, err)
	result = map[string]any{}
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		for k, v := range chunk {
			result[k] = v
		}
	}
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)
}

func TestStreamChainMultiBranch(t *testing.T) {
	emptyLambda := InvokableLambda(func(ctx context.Context, input string) (output string, err error) { return input, nil })

	ctx := context.Background()
	r, err := NewChain[string, map[string]any]().
		AppendBranch(NewStreamChainMultiBranch(func(ctx context.Context, in *schema.StreamReader[string]) (endNode map[string]bool, err error) {
			return map[string]bool{"1": true, "2": true}, nil
		}).AddLambda("1", emptyLambda, WithOutputKey("1")).AddLambda("2", emptyLambda, WithOutputKey("2")).AddLambda("3", emptyLambda, WithOutputKey("3"))).
		Compile(ctx)
	assert.Nil(t, err)

	result, err := r.Invoke(ctx, "start")
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)

	streamResult, err := r.Stream(ctx, "start")
	assert.NoError(t, err)
	result = map[string]any{}
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		for k, v := range chunk {
			result[k] = v
		}
	}
	assert.Equal(t, map[string]any{
		"1": "start",
		"2": "start",
	}, result)
}
