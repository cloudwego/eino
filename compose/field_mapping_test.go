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

package compose

import (
	"reflect"
	"testing"
)

func TestValidateStructOrMap(t *testing.T) {
	type S struct {
		A int
	}
	type M map[string]any

	tests := []struct {
		name string
		typ  reflect.Type
		want bool
	}{
		{"struct", reflect.TypeOf(S{}), true},
		{"map", reflect.TypeOf(M{}), true},
		{"ptr_to_struct", reflect.TypeOf(&S{}), true},
		{"ptr_to_map", reflect.TypeOf(&M{}), true},
		{"ptr_to_scalar", reflect.TypeOf((*int)(nil)), false},
		{"scalar", reflect.TypeOf(0), false},
		{"ptr_to_string", reflect.TypeOf((*string)(nil)), false},
		{"slice", reflect.TypeOf([]int{}), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateStructOrMap(tt.typ); got != tt.want {
				t.Errorf("validateStructOrMap(%v) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}
