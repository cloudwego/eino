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

package filesystem_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/adk/filesystem"
)

func TestGrepRawMultilineMatchingLines(t *testing.T) {
	tests := []struct {
		name, content, pattern string
		want                   []int
	}{
		{"multiple matches on one line", "hit hit\nunrelated", "hit", []int{1}},
		{"adjacent multiline matches share a line", "a\nb a\nb", "a\nb", []int{1, 2, 3}},
		{"match ends with newline", "start\nend\nunrelated", "start\nend\n", []int{1, 2}},
		{"newline only", "one\ntwo", "\n", []int{1}},
		{"match ends at file end", "start\nend", "start\nend", []int{1, 2}},
		{"disjoint matching lines", "hit\nunrelated\nhit", "hit", []int{1, 3}},
		{"empty match at start", "one\ntwo", "^", []int{1}},
		{"empty match at end", "one\ntwo", "$", []int{2}},
		{"no matches", "one\ntwo", "absent", nil},
	}
	for _, parallel := range []bool{false, true} {
		for _, tc := range tests {
			t.Run(tc.name+map[bool]string{false: "/single", true: "/parallel"}[parallel], func(t *testing.T) {
				ctx := context.Background()
				b := filesystem.NewInMemoryBackend()
				if err := b.Write(ctx, &filesystem.WriteRequest{FilePath: "/a.txt", Content: tc.content}); err != nil {
					t.Fatal(err)
				}
				if parallel {
					if err := b.Write(ctx, &filesystem.WriteRequest{FilePath: "/z.txt", Content: tc.content}); err != nil {
						t.Fatal(err)
					}
				}
				matches, err := b.GrepRaw(ctx, &filesystem.GrepRequest{Pattern: tc.pattern, EnableMultiline: true})
				if err != nil {
					t.Fatal(err)
				}
				var got []int
				for _, m := range matches {
					if m.Path == "/a.txt" {
						got = append(got, m.Line)
					}
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("matching lines = %v, want %v; results = %+v", got, tc.want, matches)
				}
				if parallel && len(matches) != 2*len(tc.want) {
					t.Errorf("matches = %d, want %d", len(matches), 2*len(tc.want))
				}
			})
		}
	}
}
