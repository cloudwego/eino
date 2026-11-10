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

package filesystem

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk/filesystem"
)

func TestGrepToolMultilineMatchingLines(t *testing.T) {
	ctx := context.Background()
	backend := filesystem.NewInMemoryBackend()
	if err := backend.Write(ctx, &filesystem.WriteRequest{FilePath: "/notes.txt", Content: "hit hit\nend\nunrelated"}); err != nil {
		t.Fatal(err)
	}
	grepTool, err := newGrepTool(backend, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"pattern":"hit|end\n","multiline":true,"output_mode":"content"}`,
		`{"pattern":"hit|end\n","multiline":true,"output_mode":"content","head_limit":2}`,
	} {
		t.Run(input, func(t *testing.T) {
			got, err := invokeTool(t, grepTool, input)
			if err != nil {
				t.Fatal(err)
			}
			want := "/notes.txt:1:hit hit\n/notes.txt:2:end"
			if got != want {
				t.Fatalf("grep content = %q, want %q", got, want)
			}
		})
	}
}
