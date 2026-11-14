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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

type ParserForTest struct {
	mock func() ([]*schema.Document, error)
}

func (p *ParserForTest) Parse(ctx context.Context, reader io.Reader, opts ...Option) ([]*schema.Document, error) {
	return p.mock()
}

func TestExtParserRoutesHTTPURLUsingPath(t *testing.T) {
	ctx := context.Background()
	p, err := NewExtParser(ctx, &ExtParserConfig{
		Parsers: map[string]Parser{
			".md": &ParserForTest{mock: func() ([]*schema.Document, error) {
				return []*schema.Document{{Content: "parsed Markdown"}}, nil
			}},
		},
	})
	require.NoError(t, err)

	tests := []struct {
		uri  string
		want string
	}{
		{"https://example.com/notes.md?download=true", "parsed Markdown"},
		{"http://example.com/notes%2Emd#heading", "parsed Markdown"},
		{"https://example.com?name=notes.md", "raw"},
		{"https://example.md", "raw"},
		{"notes#draft.md", "parsed Markdown"},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			docs, err := p.Parse(ctx, strings.NewReader("raw"), WithURI(tt.uri))
			require.NoError(t, err)
			require.Len(t, docs, 1)
			assert.Equal(t, tt.want, docs[0].Content)
		})
	}
}

func TestParser(t *testing.T) {
	ctx := context.Background()

	t.Run("Test default parser", func(t *testing.T) {
		conf := &ExtParserConfig{}

		p, err := NewExtParser(ctx, conf)
		if err != nil {
			t.Fatal(err)
		}

		f, err := os.Open("testdata/test.md")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		docs, err := p.Parse(ctx, f, WithURI("testdata/test.md"))
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
