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
	"fmt"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/generic"
)

type nodeOptionsPair generic.Pair[*GraphNode, *GraphAddNodeOpts]

type ChainBranch struct {
	key2BranchNode map[string]nodeOptionsPair
	condition      *composableRunnable
	err            error
}

func NewChainBranch[T any](cond func(ctx context.Context, in T) (endNode string, err error)) *ChainBranch {
	invokeCond := func(ctx context.Context, in T, opts ...any) (endNode string, err error) {
		return cond(ctx, in)
	}

	return &ChainBranch{
		key2BranchNode: make(map[string]nodeOptionsPair),
		condition:      runnableLambda(invokeCond, nil, nil, nil, false),
	}
}

func NewStreamChainBranch[T any](cond func(ctx context.Context, in *schema.StreamReader[T]) (endNode string, err error)) *ChainBranch {
	collectCon := func(ctx context.Context, in *schema.StreamReader[T], opts ...any) (endNode string, err error) {
		return cond(ctx, in)
	}

	return &ChainBranch{
		key2BranchNode: make(map[string]nodeOptionsPair),
		condition:      runnableLambda(nil, nil, collectCon, nil, false),
	}
}

func (cb *ChainBranch) AddNode(key string, node *GraphNode, options *GraphAddNodeOpts) *ChainBranch {
	if cb.err != nil {
		return cb
	}

	if cb.key2BranchNode == nil {
		cb.key2BranchNode = make(map[string]nodeOptionsPair)
	}

	_, ok := cb.key2BranchNode[key]
	if ok {
		cb.err = fmt.Errorf("chain branch add node, duplicate branch node key= %s", key)
		return cb
	}

	cb.key2BranchNode[key] = nodeOptionsPair{First: node, Second: options} // nolint: byted_use_map_without_nilcheck

	return cb
}
