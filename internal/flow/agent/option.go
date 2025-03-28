/*
 * Copyright 2025 CloudWeGo Authors
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

package agent

import (
	"github.com/cloudwego/eino/compose"
)

// DesignateNodePrependPath prepends the prefix to the path of the node(s) to which the option will be applied to.
// Useful when you already have an Option designated to a graph's node, and now you want to add this graph as a subgraph.
// e.g.
// Your subgraph has a Node with key "A", and your subgraph's NodeKey is "sub_graph", you can specify option to A using:
//
// option := WithCallbacks(...).DesignateNode("A").DesignateNodePrependPath("sub_graph")
// Note: as an End User, you probably don't need to use this method, as DesignateNodeWithPath will be sufficient in most use cases.
// Note: as a Flow author, if you define your own Option type, and at the same time your flow can be exported to graph and added as GraphNode,
// you can use this method to prepend your Option's designated path with the GraphNode's path.
func DesignateNodePrependPath(o compose.Option, prefix *compose.NodePath) compose.Option {
	if prefix == nil || len(prefix.Path()) == 0 {
		return o
	}

	paths := o.Paths()
	if len(paths) == 0 {
		return o.DesignateNodeWithPath(prefix)
	}

	for i := range paths {
		paths[i] = paths[i].Prepend(prefix)
	}

	return o
}
