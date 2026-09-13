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

package parser

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

type ParserForTest struct {
	mock func() ([]*schema.Document, error)
}

func (p *ParserForTest) Parse(ctx context.Context, reader io.Reader, opts ...Option) ([]*schema.Document, error) {
	return p.mock()
}

func TestParser(t *testing.T) {
	ctx := context.Background()

	t.Run("Test default parser", func(t *testing.T) {
		conf := &ExtParserConfig{}

		p, err := NewExtParser(ctx, conf)
		if err != nil {
			t.Fatal(err)
		}

		mdPath := filepath.Join(t.TempDir(), "test.md")
		// Write the fixture with explicit LF line endings: the checked-in
		// testdata file may carry CRLF endings depending on the local git
		// configuration (core.autocrlf=true, the default on Windows), which
		// would make the content assertion below checkout-dependent.
		if err = os.WriteFile(mdPath, []byte("# Title\nhello world"), 0o644); err != nil {
			t.Fatal(err)
		}

		f, err := os.Open(mdPath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		docs, err := p.Parse(ctx, f, WithURI(mdPath))
		if err != nil {
			t.Fatal(err)
		}

		assert.Equal(t, 1, len(docs))
		assert.Equal(t, "# Title\nhello world", docs[0].Content)
	})

	t.Run("test types", func(t *testing.T) {
		mockParser := &ParserForTest{
			mock: func() ([]*schema.Document, error) {
				return []*schema.Document{
					{
						Content: "hello world",
						MetaData: map[string]any{
							"type": "text",
						},
					},
				}, nil
			},
		}

		conf := &ExtParserConfig{
			Parsers: map[string]Parser{
				".md": mockParser,
			},
		}

		p, err := NewExtParser(ctx, conf)
		if err != nil {
			t.Fatal(err)
		}

		f, err := os.Open("testdata/test.md")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		docs, err := p.Parse(ctx, f, WithURI("x/test.md"))
		if err != nil {
			t.Fatal(err)
		}

		assert.Equal(t, 1, len(docs))
		assert.Equal(t, "hello world", docs[0].Content)
		assert.Equal(t, "text", docs[0].MetaData["type"])
	})

	t.Run("test get parsers", func(t *testing.T) {
		p, err := NewExtParser(ctx, &ExtParserConfig{
			Parsers: map[string]Parser{
				".md": &TextParser{},
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		ps := p.GetParsers()
		assert.Equal(t, 1, len(ps))
	})
}
