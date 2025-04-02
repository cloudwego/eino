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
	"github.com/cloudwego/eino/components"
)

type component = components.Component

// built-in component types in graph node.
// it represents the type of the most primitive executable object provided by the user.
const (
	ComponentOfUnknown     component = "Unknown"
	ComponentOfGraph       component = "Graph"
	ComponentOfWorkflow    component = "Workflow"
	ComponentOfChain       component = "Chain"
	ComponentOfPassthrough component = "Passthrough"
	ComponentOfToolsNode   component = "ToolsNode"
	ComponentOfLambda      component = "Lambda"
)

// NodeTriggerMode controls the triggering mode of graph nodes.
type NodeTriggerMode string

const (
	// AnyPredecessor means that the node will be triggered when any of its predecessors is included in the previous completed super step.
	// Ref:https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/orchestration_design_principles/#runtime-engine
	AnyPredecessor NodeTriggerMode = "any_predecessor"
	// AllPredecessor means that the current node will only be triggered when all of its predecessor nodes have finished running.
	AllPredecessor NodeTriggerMode = "all_predecessor"
)
