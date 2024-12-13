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

// GraphCompileOption options for compiling AnyGraph.
type GraphCompileOption func(*graphCompileOptions)

func WithMaxRunSteps(maxSteps int) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.maxRunSteps = maxSteps
	}
}

func WithGraphName(graphName string) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.graphName = graphName
	}
}

func WithGraphKey(graphKey string) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.graphKey = graphKey
	}
}

// WithNodeTriggerMode sets node trigger mode for the graph.
// Different node trigger mode will affect graph execution order and result for specific graphs, such as those with parallel branches having different length of nodes.
func WithNodeTriggerMode(triggerMode NodeTriggerMode) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.nodeTriggerMode = triggerMode
	}
}

// WithGraphCompileCallbacks sets callbacks for graph compilation.
func WithGraphCompileCallbacks(cbs ...GraphCompileCallback) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.callbacks = append(o.callbacks, cbs...)
	}
}

// withComponent sets the component type of the graph. ONLY FOR INTERNAL.
func withComponent(component component) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.component = component
	}
}

// InitGraphCompileCallbacks set global graph compile callbacks,
// which ONLY will be added to top level graph compile options
func InitGraphCompileCallbacks(cbs []GraphCompileCallback) {
	globalGraphCompileCallbacks = cbs
}

var globalGraphCompileCallbacks []GraphCompileCallback