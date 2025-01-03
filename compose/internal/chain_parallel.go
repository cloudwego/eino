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
	"fmt"
)

func NewParallel() *Parallel {
	return &Parallel{
		outputKeys: make(map[string]bool),
	}
}

type Parallel struct {
	nodes      []nodeOptionsPair
	outputKeys map[string]bool
	err        error
}

func (p *Parallel) AddNode(outputKey string, node *GraphNode, options *GraphAddNodeOpts) *Parallel {
	if p.err != nil {
		return p
	}

	if node == nil {
		p.err = fmt.Errorf("chain parallel add node invalid, node is nil")
		return p
	}

	if p.outputKeys == nil {
		p.outputKeys = make(map[string]bool)
	}

	if _, ok := p.outputKeys[outputKey]; ok {
		p.err = fmt.Errorf("parallel add node err, duplicate output key= %s", outputKey)
		return p
	}

	if node.nodeInfo == nil {
		p.err = fmt.Errorf("chain parallel add node invalid, nodeInfo is nil")
		return p
	}

	node.nodeInfo.outputKey = outputKey
	p.nodes = append(p.nodes, nodeOptionsPair{First: node, Second: options})
	p.outputKeys[outputKey] = true // nolint: byted_use_struct_without_nilcheck, byted_use_map_without_nilcheck
	return p
}
