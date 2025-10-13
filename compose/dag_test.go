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
	"testing"
)

func TestDAG(t *testing.T) {
	var err error

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}), WithOutputKey("1"))
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}), WithOutputKey("2"))
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input map[string]any) (output string, err error) {
		if _, ok := input["1"]; !ok {
			return "", fmt.Errorf("node 1 output fail: %+v", input)
		}
		if _, ok := input["2"]; !ok {
			return "", fmt.Errorf("node 2 output fail: %+v", input)
		}
		return input["1"].(string) + input["2"].(string), nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddLambdaNode("4", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddLambdaNode("5", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddLambdaNode("6", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}), WithOutputKey("6"))
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddLambdaNode("7", InvokableLambda(func(ctx context.Context, input map[string]any) (output string, err error) {
		if _, ok := input["1"]; !ok {
			return "", fmt.Errorf("7:node 1 output fail: %+v", input)
		}
		if _, ok := input["6"]; !ok {
			return "", fmt.Errorf("7:node 6 output fail: %+v", input)
		}
		return input["1"].(string) + input["6"].(string), nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEdge("1", "3")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("2", "3")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("3", "4")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("4", "5")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("4", "6")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("6", "7")
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddEdge("1", "7")
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
	err = g.AddEdge("7", END)
	if err != nil {
		t.Fatal(err)
	}

	runner, err := g.Compile(context.Background(), WithNodeTriggerMode(AllPredecessor))
	if err != nil {
		t.Fatal(err)
	}

	// success
	ctx := context.Background()
	out, err := runner.Invoke(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hellohellohello" {
		t.Fatalf("node7 fail")
	}

	result, err := runner.Invoke(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	if result != "111" {
		t.Fatalf("runner invoke fail, output: %s", result)
	}
	streamResult, err := runner.Stream(ctx, "1")
	if err != nil {
		t.Fatal(err)
	}
	defer streamResult.Close()
	ret := ""
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		ret += chunk
	}
	if ret != "111" {
		t.Fatalf("runner stream fail, output: %s", ret)
	}

	// loop
	gg := NewGraph[string, map[string]any]()
	err = gg.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}), WithOutputKey("1"))
	if err != nil {
		t.Fatal(err)
	}

	err = gg.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input map[string]any) (output string, err error) {
		return input["1"].(string), nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = gg.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	}), WithOutputKey("3"))
	if err != nil {
		t.Fatal(err)
	}

	err = gg.AddEdge("1", "2")
	if err != nil {
		t.Fatal(err)
	}
	err = gg.AddEdge("2", "3")
	if err != nil {
		t.Fatal(err)
	}
	err = gg.AddEdge("3", "2")
	if err != nil {
		t.Fatal(err)
	}
	err = gg.AddEdge(START, "1")
	if err != nil {
		t.Fatal(err)
	}
	err = gg.AddEdge("3", END)
	if err != nil {
		t.Fatal(err)
	}

	_, err = gg.compile(ctx, &graphCompileOptions{nodeTriggerMode: AllPredecessor})
	if err == nil {
		t.Fatal("cannot validate loop")
	}
}
