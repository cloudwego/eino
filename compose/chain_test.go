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
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/internal/mock/components/document"
	"github.com/cloudwego/eino/internal/mock/components/embedding"
	"github.com/cloudwego/eino/internal/mock/components/indexer"
	"github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/internal/mock/components/retriever"
	"github.com/cloudwego/eino/schema"
)

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
		})).
		AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			t.Log("in view lambda 02: ", kvs)
			return kvs, nil
		}), WithNodeKey("xlam"))

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

func TestParallelMultiNodes(t *testing.T) {
	ctx := context.Background()
	p := NewParallel()
	p.AddLambda("lambda", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
		return kvs, nil
	}))
	p.AddGraph("graph", NewChain[map[string]any, map[string]any]())
	p.AddIndexer("indexer", indexer.NewMockIndexer(gomock.NewController(t)))
	p.AddLoader("loader", document.NewMockLoader(gomock.NewController(t)))
	p.AddDocumentTransformer("document transformer", document.NewMockTransformer(gomock.NewController(t)))
	p.AddRetriever("retriever", retriever.NewMockRetriever(gomock.NewController(t)))
	p.AddChatModel("chatmodel", model.NewMockChatModel(gomock.NewController(t)))
	p.AddChatTemplate("chatTemplate", prompt.FromMessages(schema.FString, schema.SystemMessage("hello")))
	p.AddEmbedding("embedding", embedding.NewMockEmbedder(gomock.NewController(t)))
	p.AddPassthrough("passthrough")
	toolsNode, err := NewToolNode(ctx, &ToolsNodeConfig{})
	assert.NoError(t, err)
	p.AddToolsNode("tools", toolsNode)

	assert.Greater(t, len(p.nodes), 6)

	ctrl := gomock.NewController(t)
	p = NewParallel()
	p.AddIndexer("key", indexer.NewMockIndexer(ctrl))
	p.AddLoader("key", document.NewMockLoader(ctrl))
	p.AddRetriever("r", retriever.NewMockRetriever(ctrl))
	assert.NotNil(t, p.err)

	p = NewParallel()
	p.addNode("k", nil, nil)
	assert.NotNil(t, p.err)

	p = &Parallel{
		outputKeys: nil,
	}
	p.addNode("k", &graphNode{}, nil)
	assert.NotNil(t, p.err)
}

type FakeLambdaOptions struct {
	Info string
}

type FakeLambdaOption func(opt *FakeLambdaOptions)

func FakeWithLambdaInfo(info string) FakeLambdaOption {
	return func(opt *FakeLambdaOptions) {
		opt.Info = info
	}
}

func TestChainWithNodeKey(t *testing.T) {
	ctx := context.Background()

	t.Run("test normal chain with node key option", func(t *testing.T) {

		chain := NewChain[map[string]any, map[string]any]()
		chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
			return kvs, nil
		}), WithNodeKey("lambda_01"))

		b := NewChainBranch(func(ctx context.Context, input map[string]any) (string, error) {
			return "lambda_02", nil
		})

		b.AddLambda("lambda_02", InvokableLambdaWithOption(func(ctx context.Context, kvs map[string]any, opts ...FakeLambdaOption) (map[string]any, error) {
			opt := &FakeLambdaOptions{}
			for _, optFn := range opts {
				optFn(opt)
			}
			kvs["lambda_02"] = opt.Info
			return kvs, nil
		}), WithNodeKey("lambda_02"))

		b.AddLambda("lambda_03", InvokableLambdaWithOption(func(ctx context.Context, kvs map[string]any, opts ...FakeLambdaOption) (map[string]any, error) {
			opt := &FakeLambdaOptions{}
			for _, optFn := range opts {
				optFn(opt)
			}
			kvs["lambda_03"] = opt.Info
			return kvs, nil
		}), WithNodeKey("lambda_03"))

		chain.AppendBranch(b)

		chain.AppendPassthrough()

		p := NewParallel()
		p.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
			return kvs["lambda_02"].(string), nil
		}))
		p.AddLambda("lambda_04", InvokableLambdaWithOption(func(ctx context.Context, kvs map[string]any, opts ...FakeLambdaOption) (string, error) {
			opt := &FakeLambdaOptions{}
			for _, optFn := range opts {
				optFn(opt)
			}
			return opt.Info, nil
		}), WithNodeKey("lambda_04"))

		p.AddLambda("lambda_05", InvokableLambdaWithOption(func(ctx context.Context, kvs map[string]any, opts ...FakeLambdaOption) (string, error) {
			opt := &FakeLambdaOptions{}
			for _, optFn := range opts {
				optFn(opt)
			}
			return opt.Info, nil
		}), WithNodeKey("lambda_05"))
		chain.AppendParallel(p)

		chain.AppendLambda(InvokableLambdaWithOption(func(ctx context.Context, kvs map[string]any, opts ...FakeLambdaOption) (map[string]any, error) {
			opt := &FakeLambdaOptions{}
			for _, optFn := range opts {
				optFn(opt)
			}
			kvs["lambda_06"] = opt.Info
			return kvs, nil
		}), WithNodeKey("lambda_06"))

		r, err := chain.Compile(ctx)
		assert.Nil(t, err)

		res, err := r.Invoke(ctx, map[string]any{},
			WithLambdaOption(FakeWithLambdaInfo("normal")),
			WithLambdaOption(FakeWithLambdaInfo("info_lambda_02")).DesignateNode("lambda_02"), // branch
			WithLambdaOption(FakeWithLambdaInfo("info_lambda_03")).DesignateNode("lambda_03"), // branch (wont run)
			WithLambdaOption(FakeWithLambdaInfo("info_lambda_05")).DesignateNode("lambda_05"), // parallel
		)
		assert.Nil(t, err)

		assert.Equal(t, "info_lambda_02", res["lambda_02"]) // transmit option with DesigateNode
		assert.Equal(t, "info_lambda_05", res["lambda_05"]) // transmit option with DesigateNode
		assert.Equal(t, "normal", res["lambda_06"])         // without DesigateNode, using default option
	})

	t.Run("test chain with node key option and error with correct error info", func(t *testing.T) {

		t.Run("compile error of chain", func(t *testing.T) {
			chain := NewChain[map[string]any, map[string]any]()
			chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
				return "123", nil
			}), WithNodeKey("lambda_01"))

			c, err := chain.Compile(ctx)
			assert.Nil(t, c)
			fmt.Printf("%+v\n", err)

			assert.Contains(t, err.Error(), "edge[lambda_01]")
		})

		t.Run("compile error of branch", func(t *testing.T) {
			t.Run("without node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()
				chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}), WithNodeKey("lambda_01"))
				b := NewChainBranch(func(ctx context.Context, input map[string]any) (string, error) {
					return "lambda_02", nil
				})
				b.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}))
				b.AddLambda("lambda_03", InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
					return "", nil
				}))
				chain.AppendBranch(b)
				c, err := chain.Compile(ctx)
				assert.Nil(t, c)
				fmt.Printf("%+v\n", err)
				assert.Contains(t, err.Error(), "edge[node_1_branch_lambda_03]") // with no node key option, will use default node key
			})

			t.Run("with node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()
				chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}), WithNodeKey("lambda_01"))

				b := NewChainBranch(func(ctx context.Context, input map[string]any) (string, error) {
					return "lambda_02", nil
				})
				b.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}), WithNodeKey("lambda_02"))

				b.AddLambda("lambda_03", InvokableLambda(func(ctx context.Context, kvs map[string]any) (string, error) {
					return "123", nil
				}), WithNodeKey("key_of_lambda_03"))

				chain.AppendBranch(b)
				c, err := chain.Compile(ctx)
				assert.Nil(t, c)
				fmt.Printf("%+v\n", err)
				assert.Contains(t, err.Error(), "edge[key_of_lambda_03]")
			})
		})

		t.Run("compile error of parallel", func(t *testing.T) {
			t.Run("without node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()
				chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}), WithNodeKey("lambda_01"))
				p := NewParallel()
				p.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}))
				p.AddLambda("lambda_03", InvokableLambda(func(ctx context.Context, v string) (string, error) {
					return "", nil
				}))

				chain.AppendParallel(p)

				c, err := chain.Compile(ctx)
				assert.Nil(t, c)
				fmt.Printf("%+v\n", err)
				assert.Contains(t, err.Error(), "to=node_1_parallel_1") // with no node key option, will use default node key
			})

			t.Run("with node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()
				chain.AppendLambda(InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}), WithNodeKey("lambda_01"))
				p := NewParallel()
				p.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return kvs, nil
				}), WithNodeKey("lambda_02"))
				p.AddLambda("lambda_03", InvokableLambda(func(ctx context.Context, v string) (string, error) {
					return "", nil
				}), WithNodeKey("key_of_lambda_03"))
				chain.AppendParallel(p)
				c, err := chain.Compile(ctx)
				assert.Nil(t, c)
				fmt.Printf("%+v\n", err)
				assert.Contains(t, err.Error(), "to=key_of_lambda_03")
			})
		})

		t.Run("invoke error", func(t *testing.T) {
			t.Run("branch with out node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()

				b := NewChainBranch(func(ctx context.Context, input map[string]any) (string, error) {
					return "lambda_01", nil
				})

				b.AddLambda("lambda_01", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, fmt.Errorf("fake error")
				}))
				b.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, nil
				}))

				chain.AppendBranch(b)
				c, err := chain.Compile(ctx)
				assert.Nil(t, err)

				_, err = c.Invoke(ctx, map[string]any{})
				fmt.Printf("%+v\n", err)
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "node_0_branch_lambda_01") // with no node key option, will use default node key
			})

			t.Run("branch with node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()
				b := NewChainBranch(func(ctx context.Context, input map[string]any) (string, error) {
					return "lambda_01", nil
				})
				b.AddLambda("lambda_01", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, fmt.Errorf("fake error")
				}), WithNodeKey("key_of_lambda_01"))
				b.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, nil
				}))

				chain.AppendBranch(b)
				c, err := chain.Compile(ctx)
				assert.Nil(t, err)
				_, err = c.Invoke(ctx, map[string]any{})
				fmt.Printf("%+v\n", err)
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "key_of_lambda_01")
			})

			t.Run("parallel with out node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()
				p := NewParallel()
				p.AddLambda("lambda_01", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, fmt.Errorf("fake error")
				}))
				p.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, nil
				}))
				chain.AppendParallel(p)
				c, err := chain.Compile(ctx)
				assert.Nil(t, err)
				_, err = c.Invoke(ctx, map[string]any{})
				fmt.Printf("%+v\n", err)
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "node_0_parallel_0") // with no node key option, will use default node key
			})

			t.Run("parallel with node key", func(t *testing.T) {
				chain := NewChain[map[string]any, map[string]any]()
				p := NewParallel()
				p.AddLambda("lambda_01", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, fmt.Errorf("fake error")
				}), WithNodeKey("key_of_lambda_01"))
				p.AddLambda("lambda_02", InvokableLambda(func(ctx context.Context, kvs map[string]any) (map[string]any, error) {
					return nil, nil
				}))
				chain.AppendParallel(p)
				c, err := chain.Compile(ctx)
				assert.Nil(t, err)
				_, err = c.Invoke(ctx, map[string]any{})
				fmt.Printf("%+v\n", err)
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "key_of_lambda_01")
			})
		})

	})

}
