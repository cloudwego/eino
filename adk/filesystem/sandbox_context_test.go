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

import (
	"context"
	"testing"
)

func TestSandboxContext(t *testing.T) {
	tests := []struct {
		name     string
		set      bool
		value    string
		expected string
	}{
		{name: "round trip", set: true, value: "dev", expected: "dev"},
		{name: "empty value", set: true, value: "", expected: ""},
		{name: "not set", set: false, value: "", expected: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.set {
				ctx = WithSandboxName(ctx, tt.value)
			}
			got := SandboxFromContext(ctx)
			if got != tt.expected {
				t.Errorf("SandboxFromContext() = %q, want %q", got, tt.expected)
			}
		})
	}
}
