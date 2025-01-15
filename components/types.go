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

// components are the basic components supported by eino.
package components

// Typer get the type name of one component's implementation
// if Typer exists, the full name of the component instance will be {Typer}{Component} by default
// recommend using Camel Case Naming Style for Typer
type Typer interface {
	GetType() string
}

func GetType(component any) (string, bool) {
	if typer, ok := component.(Typer); ok {
		return typer.GetType(), true
	}

	return "", false
}

// Checker tells callback aspect status of component's implementation
// When the Checker interface is implemented and returns true, the framework will not start the default aspect.
// Instead, the component will decide the callback execution location and the information to be injected.
type Checker interface {
	IsCallbacksEnabled() bool
}

func IsCallbacksEnabled(i any) bool {
	if checker, ok := i.(Checker); ok {
		return checker.IsCallbacksEnabled()
	}

	return false
}

// Component the name of different kinds of components
type Component string

const (
	ComponentOfPrompt      Component = "ChatTemplate"
	ComponentOfChatModel   Component = "ChatModel"
	ComponentOfEmbedding   Component = "Embedding"
	ComponentOfIndexer     Component = "Indexer"
	ComponentOfRetriever   Component = "Retriever"
	ComponentOfLoader      Component = "Loader"
	ComponentOfTransformer Component = "DocumentTransformer"
	ComponentOfTool        Component = "Tool"
)
