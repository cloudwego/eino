/*
 * Copyright 2026 CloudWeGo Authors
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

// Package internal provides adk internal utils.
package internal

import "sync/atomic"

var useChinese atomic.Value

// SetUseChinese sets whether to use Chinese prompts globally.
func SetUseChinese() {
	useChinese.Store(true)
}

// UseChinese returns whether to use Chinese prompt
func UseChinese() bool {
	b, ok := useChinese.Load().(bool)
	return ok && b
}
