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

package serialization

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"reflect"
)

type GobSerializer struct{}

func (g *GobSerializer) Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("gob marshal error: %w", err)
	}
	return buf.Bytes(), nil
}

func (g *GobSerializer) Unmarshal(data []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("unmarshal destination must be a non-nil pointer")
	}

	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("gob unmarshal error: %w", err)
	}
	return nil
}
