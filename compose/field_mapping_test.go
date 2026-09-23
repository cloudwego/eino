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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConvertToMapValueMultipleSubFields(t *testing.T) {
	type sub struct {
		X string
		Y string
	}

	// Two mappings target sub-fields of the same map value. The second one
	// reads the value back with MapIndex (which is not addressable), so
	// assigning to its field used to panic with "reflect.Value.Set using
	// unaddressable value".
	aX, aY := FieldPath{"a", "X"}, FieldPath{"a", "Y"}
	got := convertTo(map[string]any{
		aX.join(): "x",
		aY.join(): "y",
	}, reflect.TypeOf(map[string]sub{}))

	assert.Equal(t, map[string]sub{"a": {X: "x", Y: "y"}}, got)
}
