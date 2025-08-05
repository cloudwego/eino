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
	"reflect"

	"github.com/cloudwego/eino/components"
)

// GraphNodeInfo the info which end users pass in when they are adding nodes to graph.
type GraphNodeInfo struct {
	Component             components.Component
	Instance              any
	GraphAddNodeOpts      []GraphAddNodeOpt
	InputType, OutputType reflect.Type // mainly for lambda, whose input and output types cannot be inferred by component type
	Name                  string
	InputKey, OutputKey   string
	GraphInfo             *GraphInfo
	Mappings              []*FieldMapping
}

// GraphInfo the info which end users pass in when they are compiling a graph.
// it is used in compile callback for user to get the node info and instance.
// you may need all details info of the graph for observation.
type GraphInfo struct {
	CompileOptions        []GraphCompileOption
	Nodes                 map[string]GraphNodeInfo // node key -> node info
	Edges                 map[string][]string      // edge start node key -> edge end node key, control edges
	DataEdges             map[string][]string
	Branches              map[string][]GraphBranch // branch start node key -> branch
	InputType, OutputType reflect.Type
	Name                  string

	NewGraphOptions []NewGraphOption
	GenStateFn      func(context.Context) any
}

// GraphCompileCallback is the callback which will be called when graph compilation finishes.
type GraphCompileCallback interface {
	OnFinish(ctx context.Context, info *GraphInfo)
}
