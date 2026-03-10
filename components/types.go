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

// Package components defines common interfaces that describe component
// types and callback capabilities used across Eino.
package components

// Typer provides a human-readable type name for a component implementation.
//
// When implemented, the component's full display name in DevOps tooling
// (visual debugger, IDE plugin, dashboards) becomes "{GetType()}{ComponentKind}"
// — e.g. "OpenAIChatModel". Use CamelCase naming.
//
// Also used by [utils.InferTool] and similar constructors to set the display
// name of tool instances.
type Typer interface {
	GetType() string
}

// GetType returns the type name for a component that implements Typer.
func GetType(component any) (string, bool) {
	if typer, ok := component.(Typer); ok {
		return typer.GetType(), true
	}

	return "", false
}

// Checker controls whether the framework's automatic callback instrumentation
// is active for a component.
//
// When IsCallbacksEnabled returns true, the framework skips its default
// OnStart/OnEnd wrapping and trusts the component to invoke callbacks itself
// at the correct points. Implement this when your component needs precise
// control over callback timing or content — for example, when streaming
// requires callbacks to fire mid-stream rather than only at completion.
type Checker interface {
	IsCallbacksEnabled() bool
}

// IsCallbacksEnabled reports whether a component implements Checker and enables callbacks.
func IsCallbacksEnabled(i any) bool {
	if checker, ok := i.(Checker); ok {
		return checker.IsCallbacksEnabled()
	}

	return false
}

// Component names representing the different categories of components.
type Component string

const (
	// ComponentOfPrompt identifies chat template components.
	ComponentOfPrompt Component = "ChatTemplate"
	// ComponentOfChatModel identifies chat model components.
	ComponentOfChatModel Component = "ChatModel"
	// ComponentOfEmbedding identifies embedding components.
	ComponentOfEmbedding Component = "Embedding"
	// ComponentOfIndexer identifies indexer components.
	ComponentOfIndexer Component = "Indexer"
	// ComponentOfRetriever identifies retriever components.
	ComponentOfRetriever Component = "Retriever"
	// ComponentOfLoader identifies loader components.
	ComponentOfLoader Component = "Loader"
	// ComponentOfTransformer identifies document transformer components.
	ComponentOfTransformer Component = "DocumentTransformer"
	// ComponentOfTool identifies tool components.
	ComponentOfTool Component = "Tool"
)
