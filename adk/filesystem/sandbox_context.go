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

package filesystem

import "context"

type sandboxNameCtxKey struct{}

// WithSandboxName returns a new context carrying the given sandbox name.
// This is set by the filesystem middleware when EnableMultiSandboxBackend is true.
// Backend/Shell/StreamingShell implementations retrieve it via SandboxFromContext.
func WithSandboxName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, sandboxNameCtxKey{}, name)
}

// SandboxFromContext returns the sandbox name from context.
// Returns empty string if not set (i.e. EnableMultiSandboxBackend is false
// or sandbox_name was not provided by the model).
func SandboxFromContext(ctx context.Context) string {
	v, _ := ctx.Value(sandboxNameCtxKey{}).(string)
	return v
}
