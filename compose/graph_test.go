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
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/schema"
)

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
	_, err = r.Invoke(ctx, in)
	assert.NoError(t, err)

	// stream
	s, err := r.Stream(ctx, in)
	assert.NoError(t, err)

	_, err = concatStreamReader(s)
	assert.NoError(t, err)

	sr, sw := schema.Pipe[map[string]any](1)
	_ = sw.Send(in, nil)
	sw.Close()

	// transform
	s, err = r.Transform(ctx, sr)
	assert.NoError(t, err)

	_, err = concatStreamReader(s)
	assert.NoError(t, err)

	// error test
	in = map[string]any{"wrong key": 1}
	_, err = r.Invoke(ctx, in)
	assert.Errorf(t, err, "could not find key: location")

	_, err = r.Stream(ctx, in)
	assert.Errorf(t, err, "could not find key: location")

	sr, sw = schema.Pipe[map[string]any](1)
	_ = sw.Send(in, nil)
	sw.Close()

	_, err = r.Transform(ctx, sr)
	assert.Errorf(t, err, "could not find key: location")
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

	_, err = r.Invoke(ctx, "how are you", WithRuntimeMaxSteps(1))
	assert.Error(t, err)
	assert.ErrorContains(t, err, "exceeds max steps")

	_, err = r.Invoke(ctx, "how are you", WithRuntimeMaxSteps(1))
	assert.Error(t, err)
	assert.ErrorContains(t, err, "exceeds max steps")

	out, err := r.Invoke(ctx, "how are you")
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

			return context.WithValue(ctx, ck, v)
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			input.Close()

			v, ok := ctx.Value(ck).(int)
			if ok {
				v++
			}

			return context.WithValue(ctx, ck, v)
		}).Build()

	// invoke
	_, err = r.Invoke(ctx, "london", WithCallbacks(cb))
	assert.NoError(t, err)

	// stream
	rs, err := r.Stream(ctx, "london", WithCallbacks(cb))
	assert.NoError(t, err)
	for {
		_, err = rs.Recv()
		if err == io.EOF {
			break
		}

		assert.NoError(t, err)
	}

	// collect
	sr, sw := schema.Pipe[string](5)
	_ = sw.Send("london", nil)
	sw.Close()

	_, err = r.Collect(ctx, sr, WithCallbacks(cb))
	assert.NoError(t, err)

	// transform
	sr, sw = schema.Pipe[string](5)
	_ = sw.Send("london", nil)
	sw.Close()

	rt, err := r.Transform(ctx, sr, WithCallbacks(cb))
	assert.NoError(t, err)
	for {
		_, err = rt.Recv()
		if err == io.EOF {
			break
		}

		assert.NoError(t, err)
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

	// test may matched passthrough
	g2 := NewGraph[any, string]()
	err = g2.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input any) (output any, err error) { return input, nil }))
	assert.NoError(t, err)
	err = g2.AddPassthroughNode("2")
	assert.NoError(t, err)
	err = g2.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input int) (output string, err error) { return strconv.Itoa(input), nil }))
	assert.NoError(t, err)
	err = g2.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g2.AddEdge("2", "3")
	assert.NoError(t, err)
	err = g2.AddEdge("1", "2")
	assert.NoError(t, err)
	err = g2.AddEdge("3", END)
	assert.NoError(t, err)
	ru, err := g2.Compile(context.Background())
	assert.NoError(t, err)
	// success
	result, err := ru.Invoke(context.Background(), 1)
	assert.NoError(t, err)
	assert.Equal(t, result, "1")
	// fail
	_, err = ru.Invoke(context.Background(), "1")
	assert.ErrorContains(t, err, "runtime type check")

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
	assert.ErrorContains(t, err, "add parallel edge failed, from=start, to=node_0_parallel_0, err: graph edge[start]-[node_0_parallel_0]: start node's output type[string] and end node's input type[compose.A] mismatch")

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

	result, err = r.Invoke(context.Background(), "start")
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

type cb struct {
	gInfo *GraphInfo
}

func (c *cb) OnFinish(ctx context.Context, info *GraphInfo) {
	c.gInfo = info
}

func TestGraphCompileCallback(t *testing.T) {
	t.Run("graph compile callback", func(t *testing.T) {
		type s struct{}

		g := NewGraph[map[string]any, map[string]any](WithGenLocalState(func(ctx context.Context) *s { return &s{} }))

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
		var ssGraphCompileOpts []GraphCompileOption
		ssGraphOpts := []GraphAddNodeOpt{WithGraphCompileOptions(ssGraphCompileOpts...)}
		err = subGraph.AddGraphNode("sub_sub_1", subSubGraph, ssGraphOpts...)
		assert.NoError(t, err)
		err = subGraph.AddEdge(START, "sub_sub_1")
		assert.NoError(t, err)
		err = subGraph.AddEdge("sub_sub_1", END)
		assert.NoError(t, err)

		subGraphCompileOpts := []GraphCompileOption{WithMaxRunSteps(2), WithGraphName("sub_graph")}
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
		opt := []GraphCompileOption{WithGraphCompileCallbacks(c), WithGraphName("top_level")}
		_, err = g.Compile(context.Background(), opt...)
		assert.NoError(t, err)
		expected := &GraphInfo{
			CompileOptions: opt,
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
					Name:       "",
				},
				"pass2": {
					Component:  ComponentOfPassthrough,
					InputType:  reflect.TypeOf(""),
					OutputType: reflect.TypeOf(""),
					Name:       "",
				},
				"sub_graph": {
					Component:        ComponentOfGraph,
					Instance:         subGraph,
					GraphAddNodeOpts: subGraphOpts,
					InputType:        reflect.TypeOf(""),
					OutputType:       reflect.TypeOf(""),
					Name:             "",
					GraphInfo: &GraphInfo{
						CompileOptions: subGraphCompileOpts,
						Nodes: map[string]GraphNodeInfo{
							"sub_sub_1": {
								Component:        ComponentOfGraph,
								Instance:         subSubGraph,
								GraphAddNodeOpts: ssGraphOpts,
								InputType:        reflect.TypeOf(""),
								OutputType:       reflect.TypeOf(""),
								Name:             "",
								GraphInfo: &GraphInfo{
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
									DataEdges: map[string][]string{
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
						DataEdges: map[string][]string{
							START:       {"sub_sub_1"},
							"sub_sub_1": {END},
						},
						Branches:   map[string][]GraphBranch{},
						InputType:  reflect.TypeOf(""),
						OutputType: reflect.TypeOf(""),
						Name:       "sub_graph",
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
			DataEdges: map[string][]string{
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
			Name:       "top_level",
		}

		stateFn := c.gInfo.GenStateFn
		assert.NotNil(t, stateFn)
		assert.Equal(t, &s{}, stateFn(context.Background()))

		assert.Equal(t, 1, len(c.gInfo.NewGraphOptions))
		c.gInfo.NewGraphOptions = nil

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
		assert.Equal(t, expected.Branches["node1"][0].endNodes, c.gInfo.Branches["node1"][0].endNodes)
		assert.Equal(t, expected.Branches["node1"][0].inputType, c.gInfo.Branches["node1"][0].inputType)

		expected.Branches["node1"] = []GraphBranch{}
		c.gInfo.Branches["node1"] = []GraphBranch{}
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

func TestDAGStart(t *testing.T) {
	g := NewGraph[map[string]any, map[string]any]()
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input map[string]any) (output map[string]any, err error) {
		return map[string]any{"1": "1"}, nil
	}))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input map[string]any) (output map[string]any, err error) {
		return input, nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", "2")
	assert.NoError(t, err)
	err = g.AddEdge(START, "2")
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)
	r, err := g.Compile(context.Background(), WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	result, err := r.Invoke(context.Background(), map[string]any{"start": "start"})
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{"start": "start", "1": "1"}, result)
}

func concatLambda(s string) *Lambda {
	return InvokableLambda(func(ctx context.Context, input string) (output string, err error) { return input + s, nil })
}
func mapLambda(k, v string) *Lambda {
	return InvokableLambda(func(ctx context.Context, input map[string]string) (output map[string]string, err error) {
		return map[string]string{
			k: v,
		}, nil
	})
}

func TestBaseDAGBranch(t *testing.T) {
	g := NewGraph[string, string]()

	err := g.AddLambdaNode("1", concatLambda("1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", concatLambda("2"))
	assert.NoError(t, err)
	err = g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		if len(in) > 3 {
			return "2", nil
		}
		return "1", nil
	}, map[string]bool{"1": true, "2": true}))
	assert.NoError(t, err)
	err = g.AddEdge("1", END)
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, "hi")
	assert.NoError(t, err)
	assert.Equal(t, "hi1", result)
}

func TestMultiDAGBranch(t *testing.T) {
	g := NewGraph[map[string]string, map[string]string]()

	err := g.AddLambdaNode("1", mapLambda("1", "1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", mapLambda("2", "2"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("3", mapLambda("3", "3"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("4", mapLambda("4", "4"))
	assert.NoError(t, err)
	err = g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in map[string]string) (endNode string, err error) {
		if len(in["input"]) > 3 {
			return "2", nil
		}
		return "1", nil
	}, map[string]bool{"1": true, "2": true}))
	err = g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in map[string]string) (endNode string, err error) {
		if len(in["input"]) > 3 {
			return "4", nil
		}
		return "3", nil
	}, map[string]bool{"3": true, "4": true}))
	assert.NoError(t, err)

	err = g.AddEdge("1", END)
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)
	err = g.AddEdge("4", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, map[string]string{"input": "hi"})
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{
		"1": "1",
		"3": "3",
	}, result)
}

func TestCrossDAGBranch(t *testing.T) {
	g := NewGraph[map[string]string, map[string]string]()

	err := g.AddLambdaNode("1", mapLambda("1", "1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", mapLambda("2", "2"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("3", mapLambda("3", "3"))
	assert.NoError(t, err)
	err = g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in map[string]string) (endNode string, err error) {
		if len(in["input"]) > 3 {
			return "2", nil
		}
		return "1", nil
	}, map[string]bool{"1": true, "2": true}))
	err = g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in map[string]string) (endNode string, err error) {
		if len(in["input"]) > 3 {
			return "3", nil
		}
		return "2", nil
	}, map[string]bool{"2": true, "3": true}))
	assert.NoError(t, err)

	err = g.AddEdge("1", END)
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, map[string]string{"input": "hi"})
	assert.NoError(t, err)
	assert.Equal(t, map[string]string{
		"1": "1",
		"2": "2",
	}, result)
}

func TestNestedDAGBranch(t *testing.T) {
	g := NewGraph[string, string]()

	err := g.AddLambdaNode("1", concatLambda("1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", concatLambda("2"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("3", concatLambda("3"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("4", concatLambda("4"))
	assert.NoError(t, err)
	err = g.AddBranch(START, NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		if len(in) > 3 {
			return "2", nil
		}
		return "1", nil
	}, map[string]bool{"1": true, "2": true}))
	err = g.AddBranch("2", NewGraphBranch(func(ctx context.Context, in string) (endNode string, err error) {
		if len(in) > 10 {
			return "4", nil
		}
		return "3", nil
	}, map[string]bool{"3": true, "4": true}))
	assert.NoError(t, err)
	err = g.AddEdge("1", END)
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)
	err = g.AddEdge("4", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, "hello")
	assert.NoError(t, err)
	assert.Equal(t, "hello23", result)
	result, err = r.Invoke(ctx, "hi")
	assert.NoError(t, err)
	assert.Equal(t, "hi1", result)
	result, err = r.Invoke(ctx, "hellohello")
	assert.NoError(t, err)
	assert.Equal(t, "hellohello24", result)
}

func TestHandlerTypeValidate(t *testing.T) {
	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state string) {
		return ""
	}))
	// passthrough pre fail
	err := g.AddPassthroughNode("1", WithStatePreHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}))
	assert.ErrorContains(t, err, "passthrough node[1]'s pre handler type isn't any")
	g.buildError = nil
	// passthrough pre fail with input key
	err = g.AddPassthroughNode("1", WithStatePreHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}), WithInputKey("input"))
	assert.ErrorContains(t, err, "node[1]'s pre handler type[string] is different from its input type[map[string]interface {}]")
	g.buildError = nil
	// passthrough post fail
	err = g.AddPassthroughNode("1", WithStatePostHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}))
	assert.ErrorContains(t, err, "passthrough node[1]'s post handler type isn't any")
	g.buildError = nil
	// passthrough post fail with input key
	err = g.AddPassthroughNode("1", WithStatePostHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}), WithInputKey("input"))
	assert.ErrorContains(t, err, "passthrough node[1]'s post handler type isn't any")
	g.buildError = nil
	// passthrough pre success
	err = g.AddPassthroughNode("1", WithStatePreHandler(func(ctx context.Context, in any, state string) (any, error) {
		return "", nil
	}))
	assert.NoError(t, err)
	// passthrough pre success with input key
	err = g.AddPassthroughNode("2", WithStatePreHandler(func(ctx context.Context, in map[string]any, state string) (map[string]any, error) {
		return nil, nil
	}), WithInputKey("input"))
	assert.NoError(t, err)
	// passthrough post success
	err = g.AddPassthroughNode("3", WithStatePostHandler(func(ctx context.Context, in any, state string) (any, error) {
		return "", nil
	}))
	assert.NoError(t, err)
	// passthrough post success with output key
	err = g.AddPassthroughNode("4", WithStatePostHandler(func(ctx context.Context, in map[string]any, state string) (map[string]any, error) {
		return nil, nil
	}), WithOutputKey("output"))
	assert.NoError(t, err)
	// common node pre fail
	err = g.AddLambdaNode("5", InvokableLambda(func(ctx context.Context, input int) (output int, err error) {
		return 0, nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}))
	assert.ErrorContains(t, err, "node[5]'s pre handler type[string] is different from its input type[int]")
	g.buildError = nil
	// common node post fail
	err = g.AddLambdaNode("5", InvokableLambda(func(ctx context.Context, input int) (output int, err error) {
		return 0, nil
	}), WithStatePostHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}))
	assert.ErrorContains(t, err, "node[5]'s post handler type[string] is different from its output type[int]")
	g.buildError = nil
	// common node pre success
	err = g.AddLambdaNode("5", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}))
	assert.NoError(t, err)
	// common node post success
	err = g.AddLambdaNode("6", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", nil
	}), WithStatePostHandler(func(ctx context.Context, in string, state string) (string, error) {
		return "", nil
	}))
	assert.NoError(t, err)
	// pre state fail
	err = g.AddLambdaNode("7", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state int) (string, error) {
		return "", nil
	}))
	assert.ErrorContains(t, err, "node[7]'s pre handler state type[int] is different from graph[string]")
	g.buildError = nil
	// post state fail
	err = g.AddLambdaNode("7", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", nil
	}), WithStatePostHandler(func(ctx context.Context, in string, state int) (string, error) {
		return "", nil
	}))
	assert.ErrorContains(t, err, "node[7]'s post handler state type[int] is different from graph[string]")
	g.buildError = nil
	// common pre success with input key
	err = g.AddLambdaNode("7", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", nil
	}), WithStatePreHandler(func(ctx context.Context, in map[string]any, state string) (map[string]any, error) {
		return nil, nil
	}), WithInputKey("input"))
	assert.NoError(t, err)
	// common post success with output key
	err = g.AddLambdaNode("8", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return "", nil
	}), WithStatePostHandler(func(ctx context.Context, in map[string]any, state string) (map[string]any, error) {
		return nil, nil
	}), WithOutputKey("output"))
	assert.NoError(t, err)
}

func TestSetFanInMergeConfig_RealStreamNode(t *testing.T) {
	for _, triggerMode := range []NodeTriggerMode{AnyPredecessor, AllPredecessor} {
		t.Run(string(triggerMode), func(t *testing.T) {
			g := NewGraph[int, map[string]any]()

			// Add two stream nodes that output streams of int slices
			err := g.AddLambdaNode("s1", StreamableLambda(func(ctx context.Context, input int) (*schema.StreamReader[int], error) {
				sr, sw := schema.Pipe[int](2)
				sw.Send(input+1, nil)
				sw.Send(input+2, nil)
				sw.Close()
				return sr, nil
			}), WithOutputKey("s1"))
			assert.NoError(t, err)
			err = g.AddLambdaNode("s2", StreamableLambda(func(ctx context.Context, input int) (*schema.StreamReader[int], error) {
				sr, sw := schema.Pipe[int](2)
				sw.Send(input+10, nil)
				sw.Send(input+20, nil)
				sw.Close()
				return sr, nil
			}), WithOutputKey("s2"))
			assert.NoError(t, err)

			// Connect edges: START -> s1, START -> s2, s1 -> END, s2 -> END
			err = g.AddEdge(START, "s1")
			assert.NoError(t, err)
			err = g.AddEdge(START, "s2")
			assert.NoError(t, err)
			err = g.AddEdge("s1", END)
			assert.NoError(t, err)
			err = g.AddEdge("s2", END)
			assert.NoError(t, err)

			r, err := g.Compile(context.Background(), WithNodeTriggerMode(triggerMode),
				WithFanInMergeConfig(map[string]FanInMergeConfig{END: {StreamMergeWithSourceEOF: true}}))
			assert.NoError(t, err)

			// Run the graph in stream mode and check for SourceEOF events
			sr, err := r.Stream(context.Background(), 1)
			assert.NoError(t, err)

			merged := make(map[string]map[int]bool)
			var sourceEOFCount int
			sourceNames := make(map[string]bool)
			for {
				m, e := sr.Recv()
				if e != nil {
					if name, ok := schema.GetSourceName(e); ok {
						sourceEOFCount++
						sourceNames[name] = true
						continue
					}
					if e == io.EOF {
						break
					}
					assert.NoError(t, e)
				}

				for k, v := range m {
					if merged[k] == nil {
						merged[k] = make(map[int]bool)
					}

					merged[k][v.(int)] = true
				}
			}

			// The merged map should contain both results
			assert.Equal(t, map[string]map[int]bool{"s1": {2: true, 3: true}, "s2": {11: true, 21: true}}, merged)
			assert.Equal(t, 2, sourceEOFCount, "should receive SourceEOF for each input stream when StreamMergeWithSourceEOF is true")
			assert.True(t, sourceNames["s1"], "should receive SourceEOF from s1")
			assert.True(t, sourceNames["s2"], "should receive SourceEOF from s2")
		})
	}
}

func TestFindLoops(t *testing.T) {
	tests := []struct {
		name       string
		startNodes []string
		chanCalls  map[string]*chanCall
		expected   [][]string
	}{
		{
			name:       "Graph without cycles",
			startNodes: []string{"A"},
			chanCalls: map[string]*chanCall{
				"A": {
					controls: []string{"B", "C"},
				},
				"B": {
					controls: []string{"D"},
				},
				"C": {
					controls: []string{"E"},
				},
				"D": {
					controls: []string{},
				},
				"E": {
					controls: []string{},
				},
			},
			expected: [][]string{},
		},
		{
			name:       "Graph with self-loop",
			startNodes: []string{"A"},
			chanCalls: map[string]*chanCall{
				"A": {
					controls: []string{"A", "B"},
				},
				"B": {
					controls: []string{},
				},
			},
			expected: [][]string{{"A", "A"}},
		},
		{
			name:       "Graph with simple cycle",
			startNodes: []string{"A", "B", "C"},
			chanCalls: map[string]*chanCall{
				"A": {
					controls: []string{"B"},
				},
				"B": {
					controls: []string{"C"},
				},
				"C": {
					controls: []string{"A"},
				},
			},
			expected: [][]string{{"A", "B", "C", "A"}},
		},
		{
			name:       "Graph with multiple cycles",
			startNodes: []string{"A", "B", "C", "D", "E", "F"},
			chanCalls: map[string]*chanCall{
				"A": {
					controls: []string{"B", "D"},
				},
				"B": {
					controls: []string{"C"},
				},
				"C": {
					controls: []string{"B"},
				},
				"D": {
					controls: []string{"E"},
				},
				"E": {
					controls: []string{"F"},
				},
				"F": {
					controls: []string{"D"},
				},
			},
			expected: [][]string{{"B", "C", "B"}, {"D", "E", "F", "D"}},
		},
		{
			name:       "Graph with branch cycle",
			startNodes: []string{"A", "C"},
			chanCalls: map[string]*chanCall{
				"A": {
					controls: []string{"B"},
					writeToBranches: []*GraphBranch{
						{
							endNodes: map[string]bool{
								"C": true,
							},
						},
					},
				},
				"B": {
					controls: []string{},
				},
				"C": {
					controls: []string{"A"},
				},
			},
			expected: [][]string{{"A", "C", "A"}},
		},
		{
			name:       "Empty graph",
			startNodes: []string{},
			chanCalls:  map[string]*chanCall{},
			expected:   [][]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loops := findLoops(tt.startNodes, tt.chanCalls)

			assert.Equal(t, len(tt.expected), len(loops))

			if len(tt.expected) > 0 {
				normalizedExpected := normalizeLoops(tt.expected)
				normalizedActual := normalizeLoops(loops)
				assert.Equal(t, normalizedExpected, normalizedActual)
			}
		})
	}
}
func normalizeLoops(loops [][]string) []string {
	result := make([]string, 0, len(loops))

	for _, loop := range loops {
		if len(loop) == 0 {
			continue
		}

		normalizedLoop := make([]string, len(loop))
		copy(normalizedLoop, loop)
		if normalizedLoop[0] != normalizedLoop[len(normalizedLoop)-1] {
			normalizedLoop = append(normalizedLoop, normalizedLoop[0])
		}

		minIdx := 0
		for i := 1; i < len(normalizedLoop)-1; i++ {
			if normalizedLoop[i] < normalizedLoop[minIdx] {
				minIdx = i
			}
		}

		canonicalLoop := ""
		for i := 0; i < len(normalizedLoop)-1; i++ {
			idx := (minIdx + i) % (len(normalizedLoop) - 1)
			canonicalLoop += normalizedLoop[idx] + ","
		}
		canonicalLoop += normalizedLoop[minIdx]

		result = append(result, canonicalLoop)
	}

	sort.Strings(result)
	return result
}

func TestPrintTasks(t *testing.T) {
	var ts []*task
	assert.Equal(t, "[]", printTask(ts))
	ts = []*task{{nodeKey: "1"}}
	assert.Equal(t, "[1]", printTask(ts))
	ts = []*task{{nodeKey: "1"}, {nodeKey: "2"}, {nodeKey: "3"}}
	assert.Equal(t, "[1, 2, 3]", printTask(ts))
}

func TestSkipBranch(t *testing.T) {
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	_ = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	_ = g.AddEdge(START, "1")
	_ = g.AddBranch("1", NewGraphMultiBranch(func(ctx context.Context, in string) (endNode map[string]bool, err error) {
		return map[string]bool{}, nil
	}, map[string]bool{"2": true}))
	_ = g.AddEdge("2", END)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	_, err = r.Invoke(ctx, "input")
	assert.ErrorContains(t, err, "[GraphRunError] no tasks to execute, last completed nodes: [1]")

	g = NewGraph[string, string]()
	_ = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	_ = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	_ = g.AddEdge(START, "1")
	_ = g.AddBranch("1", NewGraphMultiBranch(func(ctx context.Context, in string) (endNode map[string]bool, err error) {
		return map[string]bool{}, nil
	}, map[string]bool{"2": true}))
	_ = g.AddEdge("2", END)
	_ = g.AddEdge(START, "2")
	r, err = g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, "input")
	assert.NoError(t, err)
	assert.Equal(t, "input", result)
}

func TestGetStateInGraphCallback(t *testing.T) {
	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (s *state) {
		return &state{}
	}))
	assert.NoError(t, g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	})))
	assert.NoError(t, g.AddEdge(START, "1"))
	assert.NoError(t, g.AddEdge("1", END))

	ctx := context.Background()
	r, err := g.Compile(ctx)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "input", WithCallbacks(&testGraphStateCallbackHandler{t: t}))
	assert.NoError(t, err)
}

type state struct {
	A string
}

type testGraphStateCallbackHandler struct {
	t *testing.T
}

func (t *testGraphStateCallbackHandler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	assert.NoError(t.t, ProcessState[*state](ctx, func(ctx context.Context, s *state) error {
		s.A = "test"
		return nil
	}))
	return ctx
}

func (t *testGraphStateCallbackHandler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	return ctx
}

func (t *testGraphStateCallbackHandler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	return ctx
}

func (t *testGraphStateCallbackHandler) OnStartWithStreamInput(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	return ctx
}

func (t *testGraphStateCallbackHandler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	return ctx
}

func TestUniqueSlice(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, uniqueSlice([]string{"a", "b", "a", "c", "b"}))
	assert.Equal(t, []string{}, uniqueSlice([]string{}))
}
