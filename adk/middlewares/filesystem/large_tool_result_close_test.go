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
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestConcatStringClosesSource(t *testing.T) {
	wantErr := errors.New("stream failed")
	for _, tc := range []struct {
		name      string
		terminal  error
		converted bool
	}{
		{name: "source EOF", terminal: io.EOF},
		{name: "source error", terminal: wantErr},
		{name: "conversion error", terminal: wantErr, converted: true},
		{name: "conversion EOF", terminal: io.EOF, converted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, writer := schema.Pipe[string](3)
			source.SetAutomaticClose()
			t.Cleanup(source.Close)
			defer writer.Close()
			writer.Send("first", nil)

			reader := source
			if tc.converted {
				writer.Send("terminal", nil)
				reader = schema.StreamReaderWithConvert(source, func(chunk string) (string, error) {
					if chunk == "terminal" {
						return "", tc.terminal
					}
					return chunk, nil
				})
			} else {
				writer.Send("", tc.terminal)
			}

			result, err := concatString(reader)
			if tc.terminal == io.EOF {
				if err != nil || result != "first" {
					t.Fatalf("concatString() = %q, %v; want first, nil", result, err)
				}
			} else if !errors.Is(err, wantErr) {
				t.Fatalf("concatString() error = %v; want %v", err, wantErr)
			}

			if !writer.Send("unconsumed", nil) {
				t.Error("concatString did not close its source stream")
			}
		})
	}
}
