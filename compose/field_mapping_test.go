/*
 * Copyright 2025 CloudWeGo Authors
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
	"io"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type fieldMappingTarget struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestBuildFieldMappingConverter_PassThroughWhenInputIsTargetType(t *testing.T) {
	conv := buildFieldMappingConverter[fieldMappingTarget]()

	in := fieldMappingTarget{Name: "jenny", Age: 30}
	out, err := conv(in)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got, ok := out.(fieldMappingTarget)
	if !ok {
		t.Fatalf("expected fieldMappingTarget, got %T", out)
	}
	if got != in {
		t.Fatalf("expected %+v, got %+v", in, got)
	}
}

func TestBuildFieldMappingConverter_MapInputStillWorks(t *testing.T) {
	conv := buildFieldMappingConverter[fieldMappingTarget]()

	out, err := conv(map[string]any{"Name": "jenny", "Age": 30})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got, ok := out.(fieldMappingTarget)
	if !ok {
		t.Fatalf("expected fieldMappingTarget, got %T", out)
	}
	if got.Name != "jenny" || got.Age != 30 {
		t.Fatalf("unexpected result %+v", got)
	}
}

func TestBuildFieldMappingConverter_UnrelatedTypePanics(t *testing.T) {
	conv := buildFieldMappingConverter[fieldMappingTarget]()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on unrelated input type")
		}
	}()
	_, _ = conv(42)
}

func TestBuildStreamFieldMappingConverter_PassThroughWhenInputIsTargetType(t *testing.T) {
	conv := buildStreamFieldMappingConverter[fieldMappingTarget]()

	items := []fieldMappingTarget{{Name: "a", Age: 1}, {Name: "b", Age: 2}}
	src := packStreamReader(schema.StreamReaderFromArray(items))

	out := conv(src)
	sr, ok := unpackStreamReader[fieldMappingTarget](out)
	if !ok {
		t.Fatalf("expected stream reader of target type")
	}

	var got []fieldMappingTarget
	for {
		v, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv err: %v", err)
		}
		got = append(got, v)
	}
	if len(got) != len(items) || got[0] != items[0] || got[1] != items[1] {
		t.Fatalf("expected %+v, got %+v", items, got)
	}
}

func TestBuildStreamFieldMappingConverter_MapInputStillWorks(t *testing.T) {
	conv := buildStreamFieldMappingConverter[fieldMappingTarget]()

	items := []map[string]any{
		{"Name": "a", "Age": 1},
		{"Name": "b", "Age": 2},
	}
	src := packStreamReader(schema.StreamReaderFromArray(items))

	out := conv(src)
	sr, ok := unpackStreamReader[fieldMappingTarget](out)
	if !ok {
		t.Fatalf("expected stream reader of target type")
	}

	var got []fieldMappingTarget
	for {
		v, err := sr.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("recv err: %v", err)
		}
		got = append(got, v)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("unexpected got %+v", got)
	}
}

func TestBuildStreamFieldMappingConverter_UnrelatedTypePanics(t *testing.T) {
	conv := buildStreamFieldMappingConverter[fieldMappingTarget]()

	src := packStreamReader(schema.StreamReaderFromArray([]int{1, 2, 3}))

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on unrelated stream chunk type")
		}
	}()
	_ = conv(src)
}
