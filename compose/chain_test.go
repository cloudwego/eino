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
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/compose/internal"
	"github.com/cloudwego/eino/internal/mock/components/embedding"
	"github.com/cloudwego/eino/internal/mock/components/indexer"
	"github.com/cloudwego/eino/internal/mock/components/retriever"
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
		assert.NotNil(t, c.Err)

		c = NewChain[string, string]()
		c.AppendBranch(NewChainBranch[string](nil))
		assert.NotNil(t, c.Err)

		c = NewChain[string, string]()
		c.AppendBranch(NewChainBranch(cond).AddChatTemplate("template", prompt.FromMessages(schema.FString, schema.SystemMessage("hello"))))
		assert.NotNil(t, c.Err)

		c = NewChain[string, string]()
		c.AppendBranch(NewChainBranch(cond).AddChatTemplate("1", prompt.FromMessages(schema.FString)).AddChatTemplate("1", prompt.FromMessages(schema.FString)))
		assert.NotNil(t, c.Err)
	})

	t.Run("different Node types in branch", func(t *testing.T) {
		c := NewChain[string, string]()
		c.AppendBranch(NewChainBranch(cond).
			AddChatTemplate("t", prompt.FromMessages(schema.FString)).
			AddGraph("c", NewChain[string, string]()))
		assert.NotNil(t, c.Err)
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
		assert.Nil(t, c.Err)
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

		assert.Nil(t, c.Err)
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

		assert.Nil(t, c.Err)
		compiledChain, err := c.Compile(context.Background())
		assert.Nil(t, err)

		out, err := compiledChain.Stream(context.Background(), "one size fit all")
		assert.Nil(t, err)
		concat, err := internal.ConcatStreamReader(out)
		assert.Nil(t, err)
		assert.Equal(t, "onesizefitallonesizefitall", concat)
	})
}

func TestChain(t *testing.T) {

	cm := &mockIntentChatModel{}

	// 构建 branch
	branchCond := func(ctx context.Context, input map[string]any) (string, error) {
		if rand.Intn(2) == 1 {
			return "b1", nil
		}
		return "b2", nil
	}

	b1 := InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
		t.Log("hello in branch lambda 01")
		kvs["role"] = "cat"
		return kvs, nil
	})
	b2 := InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
		t.Log("hello in branch lambda 02")
		kvs["role"] = "dog"
		return kvs, nil
	})

	// 并发节点
	parallel := NewParallel()
	parallel.
		AddLambda("role", InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
			// may be change role to others by input kvs, for example (dentist/doctor...)
			role := kvs["role"]
			if role.(string) == "" {
				role = "bird"
			}
			return role.(string), nil
		})).
		AddLambda("input", InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
			return "你的叫声是怎样的？", nil
		}))

	// 顺序节点
	rolePlayChain := NewChain[map[string]any, *schema.Message]()
	rolePlayChain.
		AppendChatTemplate(prompt.FromMessages(schema.FString, schema.SystemMessage(`You are a {role}.`), schema.UserMessage(`{input}`))).
		AppendChatModel(cm)

	// 构建 chain

	chain := NewChain[map[string]any, string]()
	chain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			// do some logic to prepare kv as variables for next Node
			// just pass through
			t.Log("in view lambda: ", kvs)
			return kvs, nil
		})).
		AppendBranch(NewChainBranch[map[string]any](branchCond).AddLambda("b1", b1).AddLambda("b2", b2)).
		AppendPassthrough().
		AppendParallel(parallel).
		AppendGraph(rolePlayChain).
		AppendLambda(InvokableLambda(func(ctx context.Context, m *schema.Message) (string, error) {
			// do some logic to check the output or something
			t.Log("in view of messages: ", m.Content)

			return m.Content, nil
		}))

	r, err := chain.Compile(context.Background())
	assert.Nil(t, err)

	out, err := r.Invoke(context.Background(), map[string]any{})
	assert.Nil(t, err)
	t.Log(err)

	t.Log("out is : ", out)
}

func TestChainWithException(t *testing.T) {
	chain := NewChain[map[string]any, string]()
	chain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			// do some logic to prepare kv as variables for next Node
			// just pass through
			t.Log("in view lambda: ", kvs)
			return kvs, nil
		}))

	// items with parallels
	parallel := NewParallel()
	parallel.
		AddLambda("hello", InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
			t.Log("in parallel item 01")
			return "world", nil
		})).
		AddLambda("world", InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
			t.Log("in parallel item 02")
			return "hello", nil
		}))

	// sequence items
	nchain := NewChain[map[string]any, map[string]any]()
	nchain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in sequence item 01")
			return kvs, nil
		})).
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in sequence item 02")
			return kvs, nil
		}))

	branchCond := func(ctx context.Context, input map[string]any) (string, error) {
		if rand.Intn(2) == 1 {
			return "b1", nil
		}
		return "b2", nil
	}

	b1 := InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
		t.Log("hello in branch lambda 01")
		kvs["role"] = "cat"
		return kvs, nil
	})
	b2 := InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
		return kvs, nil
	})

	// sequence with branch
	chain.AppendBranch(NewChainBranch[map[string]any](branchCond).AddLambda("b1", b1).AddLambda("b2", b2))

	// parallel with sequence
	parallel.AddGraph("test_sequence", nchain)

	// parallel with parallel
	npara := NewParallel().
		AddLambda("test_parallel1", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		})).
		AddLambda("test_parallel2", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))

	// parallel with graph
	ngraph := NewChain[map[string]any, map[string]any]().
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in graph item 01")
			return kvs, nil
		})).
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in graph item 02")
			return kvs, nil
		}))
	nc := NewChain[map[string]any, map[string]any]()
	nc.AppendGraph(ngraph)
	parallel.AddGraph("test_graph", nc)

	chain.AppendPassthrough()

	// sequence with parallel
	chain.AppendParallel(npara)

	// 构建 chain
	chain.
		AppendGraph(nchain).
		AppendParallel(parallel).
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
			t.Log("in last view lambda: ", kvs)
			return "hello last", nil
		}))

	ctx := context.Background()

	r, err := chain.Compile(ctx)
	assert.Nil(t, err)

	out, err := r.Invoke(ctx, map[string]any{"test": "test"})
	assert.Nil(t, err)
	t.Log("out is : ", out)
}

func TestEmptyList(t *testing.T) {
	ctx := context.Background()

	// no nodes in chain
	chain := NewChain[map[string]any, map[string]any]()
	_, err := chain.Compile(ctx)
	assert.Error(t, err)

	// no nodes in parallel
	parallel := NewParallel()
	chain = NewChain[map[string]any, map[string]any]()
	chain.AppendParallel(parallel)

	_, err = chain.Compile(ctx)
	assert.Error(t, err)

	// no nodes in sequence
	emptyChain := NewChain[map[string]any, map[string]any]()
	chain = NewChain[map[string]any, map[string]any]()

	chain.
		AppendParallel(parallel).
		AppendGraph(emptyChain).
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))

	_, err = chain.Compile(ctx)
	assert.Error(t, err)
}

func TestChainList(t *testing.T) {
	chain := NewChain[map[string]any, map[string]any]()
	chain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in view lambda: ", kvs)
			return kvs, nil
		}))

	// parallel
	parallel := NewParallel()
	parallel.
		AddLambda("test_parallel1", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in parallel item 01")
			return kvs, nil
		}))

	// seq in parallel
	nchain := NewChain[map[string]any, map[string]any]()
	nchain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in sequence in parallel item 01")
			return kvs, nil
		})).
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in sequence in parallel item 02")
			return kvs, nil
		}))

	// seq in seq
	nchainInChain := NewChain[map[string]any, map[string]any]()
	nchainInChain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in sequence in sequence item 01")
			return kvs, nil
		})).
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in sequence in sequence item 02")
			return kvs, nil
		}))

	nchain.AppendGraph(nchainInChain)

	parallel.AddGraph("test_seq_in_parallel", nchain)

	chain.AppendParallel(parallel)

	r, err := chain.Compile(context.Background())
	assert.Nil(t, err)
	out, err := r.Invoke(context.Background(), map[string]any{"test": "test"})
	assert.Nil(t, err)
	t.Log("out is : ", out)
}

func TestChainSingleNode(t *testing.T) {
	chain := NewChain[map[string]any, map[string]any]()
	chain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in view lambda: ", kvs)
			return kvs, nil
		}))

	// single Node in chain (prepare for parallel)
	singleNodeChain := NewChain[map[string]any, map[string]any]()
	singleNodeChain.
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in sequence item 01")
			return kvs, nil
		}))

	// add parallel
	parallel := NewParallel()
	parallel.
		AddLambda("test_parallel1_lambda", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in parallel item 01")
			return kvs, nil
		}))

	parallel.AddGraph("test_parallel2_chain", singleNodeChain)

	ctx := context.Background()

	chain.AppendParallel(parallel)
	r, err := chain.Compile(ctx)
	assert.Nil(t, err)

	out, err := r.Invoke(ctx, map[string]any{"test": "test"})
	assert.Nil(t, err)
	t.Log("out is : ", out)
}

func TestParallelModels(t *testing.T) {
	cm := &mockIntentChatModel{}
	chain := NewChain[map[string]any, map[string]any]()
	chatSuite := NewChain[map[string]any, string]()
	chatSuite.
		AppendChatTemplate(prompt.FromMessages(schema.FString, schema.SystemMessage(`You are a {role}.`), schema.UserMessage(`{input}`))).
		AppendChatModel(cm).
		AppendLambda(InvokableLambda(func(ctx context.Context, msg *schema.Message) (string, error) {
			t.Log("in parallel item 01")
			return msg.Content, nil
		}))

	parallel := NewParallel()
	parallel.
		AddGraph("time001", chatSuite).
		AddGraph("time002", chatSuite).
		AddGraph("time003", chatSuite)

	chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
		t.Log("in view lambda: ", kvs)
		return kvs, nil
	}))

	chain.AppendParallel(parallel)

	ctx := context.Background()

	r, err := chain.Compile(ctx)
	assert.Nil(t, err)

	out, err := r.Invoke(ctx, map[string]any{"role": "cat", "input": "你怎么叫的？"})
	assert.Nil(t, err)

	t.Log("out is : ", out)
}

func TestChainMultiNodes(t *testing.T) {
	ctx := context.Background()

	t.Run("test embedding Node", func(t *testing.T) {
		chain := NewChain[[]string, [][]float64]()

		mockCtrl := gomock.NewController(t)
		eb := embedding.NewMockEmbedder(mockCtrl)
		chain.AppendEmbedding(eb)

		r, err := chain.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("test retriever Node", func(t *testing.T) {
		chain := NewChain[string, []*schema.Document]()

		chain.AppendRetriever(retriever.NewMockRetriever(gomock.NewController(t)))

		r, err := chain.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("test chat model", func(t *testing.T) {
		chain := NewChain[[]*schema.Message, *schema.Message]()

		cm := &mockIntentChatModel{}
		chain.AppendChatModel(cm)

		r, err := chain.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("test chat template", func(t *testing.T) {
		chain := NewChain[map[string]any, []*schema.Message]()

		chatTemplate := prompt.FromMessages(schema.FString)
		chain.AppendChatTemplate(chatTemplate)

		r, err := chain.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("test lambda", func(t *testing.T) {
		chain := NewChain[map[string]any, map[string]any]()

		chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))

		r, err := chain.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("test indexer", func(t *testing.T) {
		chain := NewChain[[]*schema.Document, []string]()

		chain.AppendIndexer(indexer.NewMockIndexer(gomock.NewController(t)))

		r, err := chain.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("test parallel", func(t *testing.T) {
		chain := NewChain[map[string]any, map[string]any]()
		parallel := NewParallel()
		parallel.AddLambda("test_parallel", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		chain.AppendParallel(parallel)
		_, err := chain.Compile(ctx)
		assert.Error(t, err)

		chain = NewChain[map[string]any, map[string]any]()
		parallel = NewParallel()
		parallel.AddLambda("test_parallel", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		parallel.AddLambda("test_parallel", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		chain.AppendParallel(parallel)
		_, err = chain.Compile(ctx)
		assert.Error(t, err)

		chain = NewChain[map[string]any, map[string]any]()
		parallel = NewParallel()
		parallel.AddLambda("test_parallel", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		parallel.AddLambda("test_parallel1", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		chain.AppendParallel(parallel)
		_, err = chain.Compile(ctx)
		assert.NoError(t, err)

		chain = NewChain[map[string]any, map[string]any]()
		parallel = NewParallel()
		parallel.AddLambda("test_parallel", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		parallel.AddLambda("test_parallel1", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		chain.AppendParallel(parallel)

		parallel1 := NewParallel()
		parallel1.AddLambda("test_parallel", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		parallel1.AddLambda("test_parallel1", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		chain.AppendParallel(parallel1)

		_, err = chain.Compile(ctx)
		assert.Error(t, err)
	})

	t.Run("test tools Node", func(t *testing.T) {
		ctx := context.Background()
		chain := NewChain[map[string]any, map[string]any]()
		toolsNode, err := NewToolNode(ctx, &ToolsNodeConfig{})
		assert.NoError(t, err)
		chain.AppendToolsNode(toolsNode)
	})

	t.Run("test chain with compile option", func(t *testing.T) {
		chain := NewChain[map[string]any, map[string]any]()
		chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}))
		r, err := chain.Compile(ctx, WithMaxRunSteps(10))
		assert.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("test chain return type", func(t *testing.T) {
		t.Run("test chain any output type", func(t *testing.T) {
			chain := NewChain[map[string]any, map[string]any]()
			chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (any, error) {
				return 1, nil
			}))
			_, err := chain.Compile(ctx)
			assert.Nil(t, err)
		})

		t.Run("test chain error output type", func(t *testing.T) {
			chain := NewChain[map[string]any, map[string]any]()
			chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
				return "123", nil
			}))
			_, err := chain.Compile(ctx)
			assert.Error(t, err)
		})

		t.Run("test chain error input type", func(t *testing.T) {
			chain := NewChain[map[string]any, map[string]any]()
			chain.AppendLambda(InvokableLambda(func(ctx context.Context, input string) (map[string]any, error) {
				return nil, nil
			}))
			_, err := chain.Compile(ctx)
			assert.Error(t, err)
		})
	})

}
