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
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
)

func TestRuntimeGraphKey(t *testing.T) {
	ctx := context.Background()
	lbd := InvokableLambda[string, string](func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	})

	t.Run("Graph", func(t *testing.T) {
		c := &cb{}
		g := NewGraph[string, string]()
		_ = g.AddLambdaNode("lbd", lbd)
		_ = g.AddEdge(START, "lbd")
		_ = g.AddEdge("lbd", END)
		_, err := g.Compile(ctx, []GraphCompileOption{WithGraphCompileCallbacks(c)}...)
		assert.NoError(t, err)
		assert.Equal(t, "github.com/cloudwego/eino/compose.TestRuntimeGraphKey.func2:43", c.gInfo.Key)
	})

	t.Run("Chain", func(t *testing.T) {
		c := &cb{}
		ch := NewChain[string, string]().AppendLambda(lbd)
		_, err := ch.Compile(ctx, []GraphCompileOption{WithGraphCompileCallbacks(c)}...)
		assert.NoError(t, err)
		assert.Equal(t, "github.com/cloudwego/eino/compose.TestRuntimeGraphKey.func3:54", c.gInfo.Key)
	})

	t.Run("StateGraph", func(t *testing.T) {
		c := &cb{}
		g := NewStateGraph[string, string, string](func(ctx context.Context) (state string) {
			return ""
		})
		_ = g.AddLambdaNode("lbd", lbd)
		_ = g.AddEdge(START, "lbd")
		_ = g.AddEdge("lbd", END)
		_, err := g.Compile(ctx, []GraphCompileOption{WithGraphCompileCallbacks(c)}...)
		assert.NoError(t, err)
		assert.Equal(t, "github.com/cloudwego/eino/compose.TestRuntimeGraphKey.func4:62", c.gInfo.Key)
	})
}

func TestSingleGraph(t *testing.T) {

	const (
		nodeOfModel  = "model"
		nodeOfPrompt = "prompt"
	)

	ctx := context.Background()
	g := NewGraph[map[string]any, *schema.Message]()

	pt := prompt.FromMessages(schema.FString,
		schema.UserMessage("what's the weather in {location}?"),
	)

	err := g.AddChatTemplateNode("prompt", pt)
	assert.NoError(t, err)

	cm := &chatModel{
		msgs: []*schema.Message{
			{
				Role:    schema.Assistant,
				Content: "the weather is good",
			},
		},
	}

	err = g.AddChatModelNode(nodeOfModel, cm, WithNodeName("MockChatModel"))
	assert.NoError(t, err)

	err = g.AddEdge(START, nodeOfPrompt)
	assert.NoError(t, err)

	err = g.AddEdge(nodeOfPrompt, nodeOfModel)
	assert.NoError(t, err)

	err = g.AddEdge(nodeOfModel, END)
	assert.NoError(t, err)

	r, err := g.Compile(context.Background(), WithMaxRunSteps(10))
	assert.NoError(t, err)

	in := map[string]any{"location": "beijing"}
	ret, err := r.Invoke(ctx, in)
	assert.NoError(t, err)
	t.Logf("invoke result: %v", ret)

	// stream
	s, err := r.Stream(ctx, in)
	assert.NoError(t, err)

	msg, err := concatStreamReader(s)
	assert.NoError(t, err)
	t.Logf("stream result: %v", msg)

	sr, sw := schema.Pipe[map[string]any](1)
	_ = sw.Send(in, nil)
	sw.Close()

	// transform
	s, err = r.Transform(ctx, sr)
	assert.NoError(t, err)

	msg, err = concatStreamReader(s)
	assert.NoError(t, err)
	t.Logf("transform result: %v", msg)

	// error test
	in = map[string]any{"wrong key": 1}
	_, err = r.Invoke(ctx, in)
	assert.Errorf(t, err, "could not find key: location")
	t.Logf("invoke error: %v", err)

	_, err = r.Stream(ctx, in)
	assert.Errorf(t, err, "could not find key: location")
	t.Logf("stream error: %v", err)

	sr, sw = schema.Pipe[map[string]any](1)
	_ = sw.Send(in, nil)
	sw.Close()

	_, err = r.Transform(ctx, sr)
	assert.Errorf(t, err, "could not find key: location")
	t.Logf("transform error: %v", err)
}

type person interface {
	Say() string
}

type doctor struct {
	say string
}

func (d *doctor) Say() string {
	return d.say
}

func TestGraphWithImplementableType(t *testing.T) {

	const (
		node1 = "1st"
		node2 = "2nd"
	)

	ctx := context.Background()

	g := NewGraph[string, string]()

	err := g.AddLambdaNode(node1, InvokableLambda(func(ctx context.Context, input string) (output *doctor, err error) {
		return &doctor{say: input}, nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode(node2, InvokableLambda(func(ctx context.Context, input person) (output string, err error) {
		return input.Say(), nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, node1)
	assert.NoError(t, err)

	err = g.AddEdge(node1, node2)
	assert.NoError(t, err)

	err = g.AddEdge(node2, END)
	assert.NoError(t, err)

	r, err := g.Compile(context.Background(), WithMaxRunSteps(10))
	assert.NoError(t, err)

	out, err := r.Invoke(ctx, "how are you", WithRuntimeMaxSteps(1))
	assert.Error(t, err)
	assert.Equal(t, ErrExceedMaxSteps, err)

	out, err = r.Invoke(ctx, "how are you", WithGraphRunOption(WithRuntimeMaxSteps(1)))
	assert.Error(t, err)
	assert.Equal(t, ErrExceedMaxSteps, err)

	out, err = r.Invoke(ctx, "how are you")
	assert.NoError(t, err)
	assert.Equal(t, "how are you", out)

	outStream, err := r.Stream(ctx, "i'm fine")
	assert.NoError(t, err)
	defer outStream.Close()

	say, err := outStream.Recv()
	assert.NoError(t, err)
	assert.Equal(t, "i'm fine", say)
}

func TestNestedGraph(t *testing.T) {
	const (
		nodeOfLambda1  = "lambda1"
		nodeOfLambda2  = "lambda2"
		nodeOfSubGraph = "sub_graph"
		nodeOfModel    = "model"
		nodeOfPrompt   = "prompt"
	)

	ctx := context.Background()
	g := NewGraph[string, *schema.Message]()
	sg := NewGraph[map[string]any, *schema.Message]()

	l1 := InvokableLambda[string, map[string]any](
		func(ctx context.Context, input string) (output map[string]any, err error) {
			return map[string]any{"location": input}, nil
		})

	l2 := InvokableLambda[*schema.Message, *schema.Message](
		func(ctx context.Context, input *schema.Message) (output *schema.Message, err error) {
			input.Content = fmt.Sprintf("after lambda 2: %s", input.Content)
			return input, nil
		})

	pt := prompt.FromMessages(schema.FString,
		schema.UserMessage("what's the weather in {location}?"),
	)

	err := sg.AddChatTemplateNode("prompt", pt)
	assert.NoError(t, err)

	cm := &chatModel{
		msgs: []*schema.Message{
			{
				Role:    schema.Assistant,
				Content: "the weather is good",
			},
		},
	}

	err = sg.AddChatModelNode(nodeOfModel, cm, WithNodeName("MockChatModel"))
	assert.NoError(t, err)

	err = sg.AddEdge(START, nodeOfPrompt)
	assert.NoError(t, err)

	err = sg.AddEdge(nodeOfPrompt, nodeOfModel)
	assert.NoError(t, err)

	err = sg.AddEdge(nodeOfModel, END)
	assert.NoError(t, err)

	err = g.AddLambdaNode(nodeOfLambda1, l1, WithNodeName("Lambda1"))
	assert.NoError(t, err)

	err = g.AddGraphNode(nodeOfSubGraph, sg, WithNodeName("SubGraphName"))
	assert.NoError(t, err)

	err = g.AddLambdaNode(nodeOfLambda2, l2, WithNodeName("Lambda2"))
	assert.NoError(t, err)

	err = g.AddEdge(START, nodeOfLambda1)
	assert.NoError(t, err)

	err = g.AddEdge(nodeOfLambda1, nodeOfSubGraph)
	assert.NoError(t, err)

	err = g.AddEdge(nodeOfSubGraph, nodeOfLambda2)
	assert.NoError(t, err)

	err = g.AddEdge(nodeOfLambda2, END)
	assert.NoError(t, err)

	r, err := g.Compile(context.Background(),
		WithMaxRunSteps(10),
		WithGraphName("GraphName"),
	)
	assert.NoError(t, err)

	ck := "depth"
	cb := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			v, ok := ctx.Value(ck).(int)
			if ok {
				v++
			}

			t.Logf("Name=%s, Component=%v, Type=%v, Depth=%d", info.Name, info.Component, info.Type, v)

			return context.WithValue(ctx, ck, v)
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			input.Close()

			v, ok := ctx.Value(ck).(int)
			if ok {
				v++
			}

			t.Logf("Name=%s, Component=%v, Type=%v, Depth=%d", info.Name, info.Component, info.Type, v)

			return context.WithValue(ctx, ck, v)
		}).Build()

	// invoke
	ri, err := r.Invoke(ctx, "london", WithNodeCallbacks(cb))
	assert.NoError(t, err)
	t.Log(ri)

	// stream
	rs, err := r.Stream(ctx, "london", WithNodeCallbacks(cb))
	assert.NoError(t, err)
	for {
		ri, err = rs.Recv()
		if err == io.EOF {
			break
		}

		assert.NoError(t, err)
		t.Log(ri)
	}

	// collect
	sr, sw := schema.Pipe[string](5)
	_ = sw.Send("london", nil)
	sw.Close()

	rc, err := r.Collect(ctx, sr, WithNodeCallbacks(cb))
	assert.NoError(t, err)
	t.Log(rc)

	// transform
	sr, sw = schema.Pipe[string](5)
	_ = sw.Send("london", nil)
	sw.Close()

	rt, err := r.Transform(ctx, sr, WithNodeCallbacks(cb))
	assert.NoError(t, err)
	for {
		ri, err = rt.Recv()
		if err == io.EOF {
			break
		}

		assert.NoError(t, err)
		t.Log(ri)
	}
}

type chatModel struct {
	msgs []*schema.Message
}

func (c *chatModel) BindTools(tools []*schema.ToolInfo) error {
	return nil
}

func (c *chatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return c.msgs[0], nil
}

func (c *chatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](len(c.msgs))
	go func() {
		for _, msg := range c.msgs {
			sw.Send(msg, nil)
		}
		sw.Close()
	}()
	return sr, nil
}

func TestValidate(t *testing.T) {
	// test unmatched nodes
	g := NewGraph[string, string]()
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) { return "", nil }))
	assert.NoError(t, err)

	err = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input int) (output string, err error) { return "", nil }))
	assert.NoError(t, err)

	err = g.AddEdge("1", "2")
	assert.ErrorContains(t, err, "graph edge[1]-[2]: start node's output type[string] and end node's input type[int] mismatch")

	// test unmatched passthrough node
	g = NewGraph[string, string]()
	err = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) { return "", nil }))
	assert.NoError(t, err)

	err = g.AddPassthroughNode("2")
	assert.NoError(t, err)

	err = g.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input int) (output string, err error) { return "", nil }))
	assert.NoError(t, err)

	err = g.AddEdge("1", "2")
	assert.NoError(t, err)

	err = g.AddEdge("2", "3")
	assert.ErrorContains(t, err, "graph edge[2]-[3]: start node's output type[string] and end node's input type[int] mismatch")

	// test unmatched graph type
	g = NewGraph[string, string]()
	err = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input int) (output string, err error) { return "", nil }))
	assert.NoError(t, err)

	err = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output int, err error) { return 0, nil }))
	assert.NoError(t, err)

	err = g.AddEdge("1", "2")
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.ErrorContains(t, err, "graph edge[start]-[1]: start node's output type[string] and end node's input type[int] mismatch")

	// sub graph implement
	type A interface {
		A()
	}
	type B interface {
		B()
	}

	type AB interface {
		A
		B
	}
	lA := InvokableLambda(func(ctx context.Context, input A) (output string, err error) { return "", nil })
	lB := InvokableLambda(func(ctx context.Context, input B) (output string, err error) { return "", nil })
	lAB := InvokableLambda(func(ctx context.Context, input string) (output AB, err error) { return nil, nil })

	p := NewParallel().AddLambda("1", lA).AddLambda("2", lB)
	c := NewChain[string, map[string]any]().AppendLambda(lAB).AppendParallel(p)
	_, err = c.Compile(context.Background())
	assert.NoError(t, err)

	// error usage
	p = NewParallel().AddLambda("1", lA).AddLambda("2", lAB)
	c = NewChain[string, map[string]any]().AppendParallel(p)
	_, err = c.Compile(context.Background())
	assert.ErrorContains(t, err, "add parallel edge[start]-[Chain[0]_Parallel[0]_Lambda] to chain failed: graph edge[start]-[Chain[0]_Parallel[0]_Lambda]: start node's output type[string] and end node's input type[compose.A] mismatch")

	// test graph output type check
	gg := NewGraph[string, A]()
	err = gg.AddLambdaNode("nodeA", InvokableLambda(func(ctx context.Context, input string) (output A, err error) { return nil, nil }))
	assert.NoError(t, err)

	err = gg.AddLambdaNode("nodeA2", InvokableLambda(func(ctx context.Context, input string) (output A, err error) { return nil, nil }))
	assert.NoError(t, err)

	err = gg.AddLambdaNode("nodeB", InvokableLambda(func(ctx context.Context, input string) (output B, err error) { return nil, nil }))
	assert.NoError(t, err)

	err = gg.AddEdge("nodeA", END)
	assert.NoError(t, err)

	err = gg.AddEdge("nodeB", END)
	assert.ErrorContains(t, err, "graph edge[nodeB]-[end]: start node's output type[compose.B] and end node's input type[compose.A] mismatch")

	err = gg.AddEdge("nodeA2", END)
	assert.ErrorContains(t, err, "graph edge[nodeB]-[end]: start node's output type[compose.B] and end node's input type[compose.A] mismatch")

	// test any type
	anyG := NewGraph[any, string]()
	err = anyG.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node1", nil }))
	assert.NoError(t, err)

	err = anyG.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node2", nil }))
	assert.NoError(t, err)

	err = anyG.AddEdge(START, "node1")
	assert.NoError(t, err)

	err = anyG.AddEdge("node1", "node2")
	assert.NoError(t, err)

	err = anyG.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	r, err := anyG.Compile(context.Background())
	assert.NoError(t, err)

	result, err := r.Invoke(context.Background(), "start")
	assert.NoError(t, err)
	assert.Equal(t, "startnode1node2", result)

	streamResult, err := r.Stream(context.Background(), "start")
	assert.NoError(t, err)

	result = ""
	for {
		chunk, err := streamResult.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
		}
		result += chunk
	}

	assert.Equal(t, "startnode1node2", result)

	// test any type runtime error
	anyG = NewGraph[any, string]()
	err = anyG.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return 123, nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = anyG.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node2", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = anyG.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = anyG.AddEdge("node1", "node2")
	if err != nil {
		t.Fatal(err)
	}
	err = anyG.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	r, err = anyG.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Invoke(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatal("test any type runtime error fail, error is nil or error doesn't contain key word runtime")
	}
	_, err = r.Stream(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatal("test any type runtime error fail, error is nil or error doesn't contain key word runtime")
	}

	// test branch any type
	// success
	g = NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node1", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node2", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node3", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddBranch("node1", NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		return "node2", nil
	}, map[string]bool{"node2": true, "node3": true}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node3", END)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ret, err := rr.Invoke(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if ret != "startnode1node2" {
		t.Fatal("test branch any type fail, result is unexpected")
	}
	streamResult, err = rr.Stream(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	ret, err = concatStreamReader(streamResult)
	if err != nil {
		t.Fatal(err)
	}
	if ret != "startnode1node2" {
		t.Fatal("test branch any type fail, result is unexpected")
	}
	// fail
	g = NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return 1 /*error type*/, nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node2", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node3", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddBranch("node1", NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		return "node2", nil
	}, map[string]bool{"node2": true, "node3": true}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node3", END)
	if err != nil {
		t.Fatal(err)
	}
	rr, err = g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = rr.Invoke(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatal("test branch any type fail, haven't report runtime error")
	}
	_, err = rr.Stream(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatal("test branch any type fail, haven't report runtime error")
	}
}

func TestValidateMultiAnyValueBranch(t *testing.T) {
	// success
	g := NewGraph[string, map[string]any]()
	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node1", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node2": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node3": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node4", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node4": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node5", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node5": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddBranch("node1", NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		return "node2", nil
	}, map[string]bool{"node2": true, "node3": true}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddBranch("node1", NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		return "node4", nil
	}, map[string]bool{"node4": true, "node5": true}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node3", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node4", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node5", END)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ret, err := rr.Invoke(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	if !ret["node2"].(bool) || !ret["node4"].(bool) {
		t.Fatal("test branch any type fail, result is unexpected")
	}
	streamResult, err := rr.Stream(context.Background(), "start")
	if err != nil {
		t.Fatal(err)
	}
	ret, err = concatStreamReader(streamResult)
	if err != nil {
		t.Fatal(err)
	}
	if !ret["node2"].(bool) || !ret["node4"].(bool) {
		t.Fatal("test branch any type fail, result is unexpected")
	}

	// fail
	g = NewGraph[string, map[string]any]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node1", nil }))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node2": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node3": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node4", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node4": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node5", InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
		return map[string]any{"node5": true}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddBranch("node1", NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		return "node2", nil
	}, map[string]bool{"node2": true, "node3": true}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddBranch("node1", NewGraphBranch(func(ctx context.Context, in int /*error type*/) (endNode string, err error) {
		return "node4", nil
	}, map[string]bool{"node4": true, "node5": true}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node3", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node4", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node5", END)
	if err != nil {
		t.Fatal(err)
	}
	rr, err = g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = rr.Invoke(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatal("test multi branch any type fail, haven't report runtime error")
	}
	_, err = rr.Stream(context.Background(), "start")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatal("test multi branch any type fail, haven't report runtime error")
	}
}

func TestAnyTypeWithKey(t *testing.T) {
	g := NewGraph[any, map[string]any]()
	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node1", nil }), WithInputKey("node1"))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output any, err error) { return input + "node2", nil }), WithOutputKey("node2"))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node1", "node2")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Invoke(context.Background(), map[string]any{"node1": "start"})
	if err != nil {
		t.Fatal(err)
	}
	if result["node2"] != "startnode1node2" {
		t.Fatal("test any type with key fail, result is unexpected")
	}

	streamResult, err := r.Stream(context.Background(), map[string]any{"node1": "start"})
	if err != nil {
		t.Fatal(err)
	}
	ret, err := concatStreamReader(streamResult)
	if err != nil {
		t.Fatal(err)
	}
	if ret["node2"] != "startnode1node2" {
		t.Fatal("test any type with key fail, result is unexpected")
	}
}

func TestInputKey(t *testing.T) {
	g := NewGraph[map[string]any, map[string]any]()
	err := g.AddChatTemplateNode("1", prompt.FromMessages(schema.FString, schema.UserMessage("{var1}")), WithOutputKey("1"), WithInputKey("1"))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddChatTemplateNode("2", prompt.FromMessages(schema.FString, schema.UserMessage("{var2}")), WithOutputKey("2"), WithInputKey("2"))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddChatTemplateNode("3", prompt.FromMessages(schema.FString, schema.UserMessage("{var3}")), WithOutputKey("3"), WithInputKey("3"))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "2")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "3")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("1", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("2", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("3", END)
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.Compile(context.Background(), WithMaxRunSteps(100))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := r.Invoke(ctx, map[string]any{
		"1": map[string]any{"var1": "a"},
		"2": map[string]any{"var2": "b"},
		"3": map[string]any{"var3": "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["1"].([]*schema.Message)[0].Content != "a" ||
		result["2"].([]*schema.Message)[0].Content != "b" ||
		result["3"].([]*schema.Message)[0].Content != "c" {
		t.Fatal("invoke different")
	}

	sr, sw := schema.Pipe[map[string]any](10)
	sw.Send(map[string]any{"1": map[string]any{"var1": "a"}}, nil)
	sw.Send(map[string]any{"2": map[string]any{"var2": "b"}}, nil)
	sw.Send(map[string]any{"3": map[string]any{"var3": "c"}}, nil)
	sw.Close()

	streamResult, err := r.Transform(ctx, sr)
	if err != nil {
		t.Fatal(err)
	}
	defer streamResult.Close()

	result = make(map[string]any)
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range chunk {
			result[k] = v
		}
	}
	if result["1"].([]*schema.Message)[0].Content != "a" ||
		result["2"].([]*schema.Message)[0].Content != "b" ||
		result["3"].([]*schema.Message)[0].Content != "c" {
		t.Fatal("transform different")
	}
}

func TestTransferTask(t *testing.T) {
	in := [][]string{
		{
			"1",
			"2",
		},
		{
			"3",
			"4",
			"5",
			"6",
		},
		{
			"5",
			"6",
			"7",
		},
		{
			"7",
			"8",
		},
		{
			"8",
		},
	}
	invertedEdges := map[string][]string{
		"1": {"3", "4"},
		"2": {"5", "6"},
		"3": {"5"},
		"4": {"6"},
		"5": {"7"},
		"7": {"8"},
	}
	in = transferTask(in, invertedEdges)

	if !reflect.DeepEqual(
		[][]string{
			{
				"1",
			},
			{
				"3",
				"2",
			},
			{
				"5",
			},
			{
				"7",
				"4",
			},
			{
				"8",
				"6",
			},
		}, in) {
		t.Fatal("not equal")
	}
}

func TestPregelEnd(t *testing.T) {
	g := NewGraph[string, string]()
	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "node1", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "node2", nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node1", END)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node1", "node2")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node2", END)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	out, err := runner.Invoke(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if out != "node1" {
		t.Fatal("graph output is unexpected")
	}
}

func TestGraphAddNodeChecker(t *testing.T) {
	t.Run("graph_checker_failed", func(t *testing.T) {
		g := NewGraph[string, string]()
		err := g.AddLambdaNode("node1",
			InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
				return "node1", nil
			}),
			WithStatePostHandler(func(ctx context.Context, out string, state string) (string, error) {
				return out, nil
			}),
		)
		assert.ErrorContains(t, err, "only StateGraph support pre/post processor")
	})

	t.Run("state_graph_checker_success", func(t *testing.T) {
		g := NewStateGraph[string, string, string](func(ctx context.Context) (state string) {
			return ""
		})
		err := g.AddLambdaNode("node1",
			InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
				return "node1", nil
			}),
			WithStatePostHandler(func(ctx context.Context, out string, state string) (string, error) {
				return out, nil
			}),
		)
		assert.NoError(t, err)
	})
}

type cb struct {
	gInfo *GraphInfo
}

func (c *cb) OnFinish(ctx context.Context, info *GraphInfo) {
	c.gInfo = info
}

func TestGraphCompileCallback(t *testing.T) {
	t.Run("graph compile callback", func(t *testing.T) {
		type s struct{}

		g := NewStateGraph[map[string]any, map[string]any, *s](func(ctx context.Context) *s { return &s{} })

		lambda := InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return "node1", nil
		})
		lambdaOpts := []GraphAddNodeOpt{WithNodeName("lambda_1"), WithInputKey("input_key")}
		err := g.AddLambdaNode("node1", lambda, lambdaOpts...)
		assert.NoError(t, err)

		err = g.AddPassthroughNode("pass1")
		assert.NoError(t, err)
		err = g.AddPassthroughNode("pass2")
		assert.NoError(t, err)

		condition := func(ctx context.Context, input string) (string, error) {
			return input, nil
		}

		branch := NewGraphBranch(condition, map[string]bool{"pass1": true, "pass2": true})
		err = g.AddBranch("node1", branch)
		assert.NoError(t, err)

		err = g.AddEdge(START, "node1")
		assert.NoError(t, err)

		lambda2 := InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return "node2", nil
		})
		lambdaOpts2 := []GraphAddNodeOpt{WithNodeName("lambda_2")}
		subSubGraph := NewGraph[string, string]()
		err = subSubGraph.AddLambdaNode("sub1", lambda2, lambdaOpts2...)
		assert.NoError(t, err)
		err = subSubGraph.AddEdge(START, "sub1")
		assert.NoError(t, err)
		err = subSubGraph.AddEdge("sub1", END)
		assert.NoError(t, err)

		subGraph := NewGraph[string, string]()
		ssGraphCompileOpts := []GraphCompileOption{WithGraphKey("k3")}
		ssGraphOpts := []GraphAddNodeOpt{WithGraphCompileOptions(ssGraphCompileOpts...)}
		err = subGraph.AddGraphNode("sub_sub_1", subSubGraph, ssGraphOpts...)
		assert.NoError(t, err)
		err = subGraph.AddEdge(START, "sub_sub_1")
		assert.NoError(t, err)
		err = subGraph.AddEdge("sub_sub_1", END)
		assert.NoError(t, err)

		subGraphCompileOpts := []GraphCompileOption{WithMaxRunSteps(2), WithGraphKey("k2")}
		subGraphOpts := []GraphAddNodeOpt{WithGraphCompileOptions(subGraphCompileOpts...)}
		err = g.AddGraphNode("sub_graph", subGraph, subGraphOpts...)
		assert.NoError(t, err)

		err = g.AddEdge("pass1", "sub_graph")
		assert.NoError(t, err)
		err = g.AddEdge("pass2", "sub_graph")
		assert.NoError(t, err)

		lambda3 := InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return "node3", nil
		})
		lambdaOpts3 := []GraphAddNodeOpt{WithNodeName("lambda_3"), WithOutputKey("lambda_3")}
		err = g.AddLambdaNode("node3", lambda3, lambdaOpts3...)
		assert.NoError(t, err)

		lambda4 := InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return "node4", nil
		})
		lambdaOpts4 := []GraphAddNodeOpt{WithNodeName("lambda_4"), WithOutputKey("lambda_4")}
		err = g.AddLambdaNode("node4", lambda4, lambdaOpts4...)
		assert.NoError(t, err)

		err = g.AddEdge("sub_graph", "node3")
		assert.NoError(t, err)
		err = g.AddEdge("sub_graph", "node4")
		assert.NoError(t, err)
		err = g.AddEdge("node3", END)
		assert.NoError(t, err)
		err = g.AddEdge("node4", END)
		assert.NoError(t, err)

		c := &cb{}
		opt := []GraphCompileOption{WithGraphCompileCallbacks(c), WithGraphKey("k1")}
		_, err = g.Compile(context.Background(), opt...)
		assert.NoError(t, err)
		expected := &GraphInfo{
			Key:            "k1",
			CompileOptions: append(opt, withComponent(g.component())),
			Nodes: map[string]GraphNodeInfo{
				"node1": {
					Component:        ComponentOfLambda,
					Instance:         lambda,
					GraphAddNodeOpts: lambdaOpts,
					InputType:        reflect.TypeOf(""),
					OutputType:       reflect.TypeOf(""),
					Name:             "lambda_1",
					InputKey:         "input_key",
				},
				"pass1": {
					Component:  ComponentOfPassthrough,
					InputType:  reflect.TypeOf(""),
					OutputType: reflect.TypeOf(""),
					Name:       "PassthroughPassthrough",
				},
				"pass2": {
					Component:  ComponentOfPassthrough,
					InputType:  reflect.TypeOf(""),
					OutputType: reflect.TypeOf(""),
					Name:       "PassthroughPassthrough",
				},
				"sub_graph": {
					Component:        ComponentOfGraph,
					Instance:         subGraph,
					GraphAddNodeOpts: subGraphOpts,
					InputType:        reflect.TypeOf(""),
					OutputType:       reflect.TypeOf(""),
					Name:             "Graph",
					GraphInfo: &GraphInfo{
						Key:            "k2",
						CompileOptions: subGraphCompileOpts,
						Nodes: map[string]GraphNodeInfo{
							"sub_sub_1": {
								Component:        ComponentOfGraph,
								Instance:         subSubGraph,
								GraphAddNodeOpts: ssGraphOpts,
								InputType:        reflect.TypeOf(""),
								OutputType:       reflect.TypeOf(""),
								Name:             "Graph",
								GraphInfo: &GraphInfo{
									Key:            "k3",
									CompileOptions: ssGraphCompileOpts,
									Nodes: map[string]GraphNodeInfo{
										"sub1": {
											Component:        ComponentOfLambda,
											Instance:         lambda2,
											GraphAddNodeOpts: lambdaOpts2,
											InputType:        reflect.TypeOf(""),
											OutputType:       reflect.TypeOf(""),
											Name:             "lambda_2",
										},
									},
									Edges: map[string][]string{
										START:  {"sub1"},
										"sub1": {END},
									},
									Branches:   map[string][]GraphBranch{},
									InputType:  reflect.TypeOf(""),
									OutputType: reflect.TypeOf(""),
								},
							},
						},
						Edges: map[string][]string{
							START:       {"sub_sub_1"},
							"sub_sub_1": {END},
						},
						Branches:   map[string][]GraphBranch{},
						InputType:  reflect.TypeOf(""),
						OutputType: reflect.TypeOf(""),
					},
				},
				"node3": {
					Component:        ComponentOfLambda,
					Instance:         lambda3,
					GraphAddNodeOpts: lambdaOpts3,
					InputType:        reflect.TypeOf(""),
					OutputType:       reflect.TypeOf(""),
					Name:             "lambda_3",
					OutputKey:        "lambda_3",
				},
				"node4": {
					Component:        ComponentOfLambda,
					Instance:         lambda4,
					GraphAddNodeOpts: lambdaOpts4,
					InputType:        reflect.TypeOf(""),
					OutputType:       reflect.TypeOf(""),
					Name:             "lambda_4",
					OutputKey:        "lambda_4",
				},
			},
			Edges: map[string][]string{
				START:       {"node1"},
				"pass1":     {"sub_graph"},
				"pass2":     {"sub_graph"},
				"sub_graph": {"node3", "node4"},
				"node3":     {END},
				"node4":     {END},
			},
			Branches: map[string][]GraphBranch{
				"node1": {*branch},
			},
			InputType:  reflect.TypeOf(map[string]any{}),
			OutputType: reflect.TypeOf(map[string]any{}),
		}

		stateFn := c.gInfo.GenStateFn
		assert.NotNil(t, stateFn)
		assert.Equal(t, &s{}, stateFn(context.Background()))

		c.gInfo.GenStateFn = nil

		actualCompileOptions := newGraphCompileOptions(c.gInfo.CompileOptions...)
		expectedCompileOptions := newGraphCompileOptions(expected.CompileOptions...)
		assert.Equal(t, len(expectedCompileOptions.callbacks), len(actualCompileOptions.callbacks))
		assert.Same(t, expectedCompileOptions.callbacks[0], actualCompileOptions.callbacks[0])
		actualCompileOptions.callbacks = nil
		actualCompileOptions.origOpts = nil
		expectedCompileOptions.callbacks = nil
		expectedCompileOptions.origOpts = nil
		assert.Equal(t, expectedCompileOptions, actualCompileOptions)

		c.gInfo.CompileOptions = nil
		expected.CompileOptions = nil
		assert.Equal(t, expected, c.gInfo)
	})
}

func TestCheckAddEdge(t *testing.T) {
	g := NewGraph[string, string]()
	err := g.AddPassthroughNode("1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddPassthroughNode("2")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("1", "2")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("1", "2")
	if err == nil {
		t.Fatal("add edge repeatedly haven't report error")
	}
}

func TestStartWithEnd(t *testing.T) {
	g := NewGraph[string, string]()
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		return END, nil
	}, map[string]bool{"1": true, END: true}))
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.Compile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sr, sw := schema.Pipe[string](1)
	sw.Send("test", nil)
	sw.Close()
	result, err := r.Transform(context.Background(), sr)
	if err != nil {
		t.Fatal(err)
	}
	for {
		chunk, err := result.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if chunk != "test" {
			t.Fatal("result is out of expect")
		}
	}
}

func TestToString(t *testing.T) {
	ps := runTypePregel.String()
	assert.Equal(t, "Pregel", ps)

	ds := runTypeDAG
	assert.Equal(t, "DAG", ds.String())
}

func TestInputKeyError(t *testing.T) {
	g := NewGraph[map[string]any, string]()
	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}), WithInputKey("node1"))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "node1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("node1", END)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := g.Compile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// invoke
	_, err = r.Invoke(ctx, map[string]any{"unknown": "123"})
	if err == nil || !strings.Contains(err.Error(), "cannot find input key: node1") {
		t.Fatal("cannot report input key error correctly")
	}

	// transform
	sr, sw := schema.Pipe[map[string]any](1)
	sw.Send(map[string]any{"unknown": "123"}, nil)
	sw.Close()
	_, err = r.Transform(ctx, sr)
	if err == nil || !strings.Contains(err.Error(), "stream reader is empty, concat fail") {
		t.Fatal("cannot report input key error correctly")
	}
}

func TestContextCancel(t *testing.T) {
	ctx := context.Background()
	g := NewGraph[string, string]()
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge(START, "1")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("1", END)
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.Compile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = r.Invoke(ctx, "test")
	if !strings.Contains(err.Error(), "context has been canceled") {
		t.Fatal("graph have not returned canceled error")
	}
}