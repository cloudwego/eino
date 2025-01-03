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

package internal

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/internal/gmap"
	"github.com/cloudwego/eino/internal/gslice"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/generic"
)

func NewChain[I, O any](opts ...NewGraphOption) *Chain[I, O] {
	ch := &Chain[I, O]{
		gg: NewGraph[I, O](opts...),
	}

	ch.gg.cmp = ComponentOfChain

	return ch
}

type Chain[I, O any] struct {
	Err error

	gg *Graph[I, O]

	namePrefix string
	nodeIdx    int

	preNodeKeys []string

	hasEnd bool
}

var ErrChainCompiled = errors.New("chain has been compiled, cannot be modified")

// implements AnyGraph.
func (c *Chain[I, O]) compile(ctx context.Context, option *graphCompileOptions) (*composableRunnable, error) {
	if err := c.addEndIfNeeded(); err != nil {
		return nil, err
	}

	return c.gg.compile(ctx, option)
}

// addEndIfNeeded add END edge of the chain/graph.
// only run once when compiling.
func (c *Chain[I, O]) addEndIfNeeded() error {
	if c.hasEnd {
		return nil
	}

	if c.Err != nil {
		return c.Err
	}

	if len(c.preNodeKeys) == 0 {
		return fmt.Errorf("pre node keys not set, number of nodes in chain= %d", len(c.gg.nodes))
	}

	for _, nodeKey := range c.preNodeKeys {
		err := c.gg.AddEdge(nodeKey, END)
		if err != nil {
			return err
		}
	}

	c.hasEnd = true

	return nil
}

// inputType returns the input type of the chain.
// implements AnyGraph.
func (c *Chain[I, O]) inputType() reflect.Type {
	return generic.TypeOf[I]()
}

// outputType returns the output type of the chain.
// implements AnyGraph.
func (c *Chain[I, O]) outputType() reflect.Type {
	return generic.TypeOf[O]()
}

// compositeType returns the composite type of the chain.
// implements AnyGraph.
func (c *Chain[I, O]) component() component {
	return c.gg.component()
}

func (c *Chain[I, O]) Compile(ctx context.Context, opts ...GraphCompileOption) (Runnable[I, O], error) {
	if err := c.addEndIfNeeded(); err != nil {
		return nil, err
	}

	return c.gg.Compile(ctx, opts...)
}

func (c *Chain[I, O]) AppendLambda(node *Lambda, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := ToLambdaNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

func (c *Chain[I, O]) AppendBranch(b *ChainBranch) *Chain[I, O] { // nolint: byted_s_too_many_lines_in_func
	if b == nil {
		c.reportError(fmt.Errorf("append branch invalid, branch is nil"))
		return c
	}

	if b.err != nil {
		c.reportError(fmt.Errorf("append branch error: %w", b.err))
		return c
	}

	if len(b.key2BranchNode) == 0 {
		c.reportError(fmt.Errorf("append branch invalid, nodeList is empty"))
		return c
	}

	if len(b.key2BranchNode) == 1 {
		c.reportError(fmt.Errorf("append branch invalid, nodeList length = 1"))
		return c
	}

	var startNode string
	if len(c.preNodeKeys) == 0 { // branch appended directly to START
		startNode = START
	} else if len(c.preNodeKeys) == 1 {
		startNode = c.preNodeKeys[0]
	} else {
		c.reportError(fmt.Errorf("append branch invalid, multiple previous nodes: %v ", c.preNodeKeys))
		return c
	}

	pName := c.nextNodeKey("Branch")
	key2NodeKey := make(map[string]string, len(b.key2BranchNode))

	for key := range b.key2BranchNode {
		node := b.key2BranchNode[key]
		nodeKey := fmt.Sprintf("%s[%s]_%s", pName, key, genNodeKeySuffix(node.First))

		if err := c.gg.AddNode(nodeKey, node.First, node.Second); err != nil {
			c.reportError(fmt.Errorf("add branch node[%s] to chain failed: %w", nodeKey, err))
			return c
		}

		key2NodeKey[key] = nodeKey
	}

	condition := &composableRunnable{
		i:                 b.condition.i,
		t:                 b.condition.t,
		inputType:         b.condition.inputType,
		inputStreamFilter: b.condition.inputStreamFilter,
		outputType:        b.condition.outputType,
		optionType:        b.condition.optionType,
		isPassthrough:     b.condition.isPassthrough,
		meta:              b.condition.meta,
		nodeInfo:          b.condition.nodeInfo,
	}

	invokeCon := func(ctx context.Context, in any, opts ...any) (endNode any, err error) {
		endKey, err := b.condition.i(ctx, in, opts...)
		if err != nil {
			return "", err
		}

		endStr, ok := endKey.(string)
		if !ok {
			return "", fmt.Errorf("chain branch result not string, got: %T", endKey)
		}

		nodeKey, ok := key2NodeKey[endStr]
		if !ok {
			return "", fmt.Errorf("chain branch result not in added keys: %s", endStr)
		}

		return nodeKey, nil
	}
	condition.i = invokeCon

	transformCon := func(ctx context.Context, sr streamReader, opts ...any) (streamReader, error) {
		iEndStream, err := b.condition.t(ctx, sr, opts...)
		if err != nil {
			return nil, err
		}

		if iEndStream.getChunkType() != reflect.TypeOf("") {
			return nil, fmt.Errorf("chain branch result not string, got: %v", iEndStream.getChunkType())
		}

		endStream, ok := unpackStreamReader[string](iEndStream)
		if !ok {
			return nil, fmt.Errorf("unpack stream reader not ok")
		}

		endStr, err := ConcatStreamReader(endStream)
		if err != nil {
			return nil, err
		}

		nodeKey, ok := key2NodeKey[endStr]
		if !ok {
			return nil, fmt.Errorf("chain branch result not in added keys: %s", endStr)
		}

		return packStreamReader(schema.StreamReaderFromArray([]string{nodeKey})), nil
	}
	condition.t = transformCon

	gBranch := &GraphBranch{
		condition: condition,
		endNodes: gslice.ToMap(gmap.Values(key2NodeKey), func(k string) (string, bool) {
			return k, true
		}),
	}

	if err := c.gg.AddBranch(startNode, gBranch); err != nil {
		c.reportError(fmt.Errorf("chain append branch failed: %w", err))
		return c
	}

	c.preNodeKeys = gmap.Values(key2NodeKey)

	return c
}

func (c *Chain[I, O]) AppendParallel(p *Parallel) *Chain[I, O] {
	if p == nil {
		c.reportError(fmt.Errorf("append parallel invalid, parallel is nil"))
		return c
	}

	if p.err != nil {
		c.reportError(fmt.Errorf("append parallel invalid, parallel error: %w", p.err))
		return c
	}

	if len(p.nodes) <= 1 {
		c.reportError(fmt.Errorf("append parallel invalid, not enough nodes, count = %d", len(p.nodes)))
		return c
	}

	var startNode string
	if len(c.preNodeKeys) == 0 { // parallel appended directly to START
		startNode = START
	} else if len(c.preNodeKeys) == 1 {
		startNode = c.preNodeKeys[0]
	} else {
		c.reportError(fmt.Errorf("append parallel invalid, multiple previous nodes: %v ", c.preNodeKeys))
		return c
	}

	pName := c.nextNodeKey("Parallel")
	var nodeKeys []string

	for i := range p.nodes {
		node := p.nodes[i]
		nodeKey := fmt.Sprintf("%s[%d]_%s", pName, i, genNodeKeySuffix(node.First))
		if err := c.gg.AddNode(nodeKey, node.First, node.Second); err != nil {
			c.reportError(fmt.Errorf("add parallel node[%s] to chain failed: %w", nodeKey, err))
			return c
		}
		if err := c.gg.AddEdge(startNode, nodeKey); err != nil {
			c.reportError(fmt.Errorf("add parallel edge[%s]-[%s] to chain failed: %w", startNode, nodeKey, err))
			return c
		}
		nodeKeys = append(nodeKeys, nodeKey)
	}

	c.preNodeKeys = nodeKeys

	return c
}

// nextNodeKey.
// get the next node key for the chain.
// e.g. "Chain[1]_ChatModel" => represent the second node of the chain, and is a ChatModel node.
// e.g. "Chain[2]_NameByUser" => represent the third node of the chain, and the node name is set by user of `NameByUser`.
func (c *Chain[I, O]) nextNodeKey(name string) string {
	if c.namePrefix == "" {
		c.namePrefix = string(ComponentOfChain)
	}
	fullKey := fmt.Sprintf("%s[%d]_%s", c.namePrefix, c.nodeIdx, name)
	c.nodeIdx++
	return fullKey
}

// reportError.
// save the first error in the chain.
func (c *Chain[I, O]) reportError(err error) {
	if c.Err == nil {
		c.Err = err
	}
}

// AddNode adds a node to the chain.
func (c *Chain[I, O]) AddNode(node *GraphNode, options *GraphAddNodeOpts) {
	if c.Err != nil {
		return
	}

	if c.gg.compiled {
		c.reportError(ErrChainCompiled)
		return
	}

	if node == nil {
		c.reportError(fmt.Errorf("chain add node invalid, node is nil"))
		return
	}

	nodeKey := options.nodeOptions.nodeKey
	if nodeKey == "" {
		nodeKey = c.nextNodeKey(genNodeKeySuffix(node))
	}

	err := c.gg.AddNode(nodeKey, node, options)
	if err != nil {
		c.reportError(err)
		return
	}

	if len(c.preNodeKeys) == 0 {
		c.preNodeKeys = append(c.preNodeKeys, START)
	}

	for _, preNodeKey := range c.preNodeKeys {
		e := c.gg.AddEdge(preNodeKey, nodeKey)
		if e != nil {
			c.reportError(e)
			return
		}
	}

	c.preNodeKeys = []string{nodeKey}
}

func genNodeKeySuffix(node *GraphNode) string {
	if len(node.nodeInfo.name) == 0 {
		return node.executorMeta.componentImplType + string(node.executorMeta.component)
	}
	return node.nodeInfo.name
}
