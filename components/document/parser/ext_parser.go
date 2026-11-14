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
	"errors"
	"io"
	"net/url"
	"path"
	"path/filepath"

	"github.com/cloudwego/eino/schema"
)

// ExtParserConfig defines the configuration for the ExtParser.
type ExtParserConfig struct {
	// ext -> parser.
	// eg: map[string]Parser{
	// 	".pdf": &PDFParser{},
	// 	".md": &MarkdownParser{},
	// }
	Parsers map[string]Parser

	// Fallback parser to use when no other parser is found.
	// Default is TextParser if not set.
	FallbackParser Parser
}

// ExtParser is a parser that uses the file extension to determine which parser to use.
// Register parsers in ExtParserConfig. Use WithURI to specify the source.
// For HTTP(S) URLs, only the path is used to select the parser. Local paths
// are handled with filepath.Ext. The default fallback is TextParser.
//
// eg:
//
//	pdf, _ := os.Open("./testdata/test.pdf")
//	docs, err := ExtParser.Parse(ctx, pdf, parser.WithURI("./testdata/test.pdf"))
type ExtParser struct {
	parsers map[string]Parser

	fallbackParser Parser
}

// NewExtParser creates a new ExtParser.
func NewExtParser(ctx context.Context, conf *ExtParserConfig) (*ExtParser, error) {
	if conf == nil {
		conf = &ExtParserConfig{}
	}

	p := &ExtParser{
		parsers:        conf.Parsers,
		fallbackParser: conf.FallbackParser,
	}

	if p.fallbackParser == nil {
		p.fallbackParser = TextParser{}
	}

	if p.parsers == nil {
		p.parsers = make(map[string]Parser)
	}

	return p, nil
}

// GetParsers returns a copy of the registered parsers.
// It is safe to modify the returned parsers.
func (p *ExtParser) GetParsers() map[string]Parser {
	res := make(map[string]Parser, len(p.parsers))
	for k, v := range p.parsers {
		res[k] = v
	}

	return res
}

// Parse parses the given reader and returns a list of documents.
func (p *ExtParser) Parse(ctx context.Context, reader io.Reader, opts ...Option) ([]*schema.Document, error) {
	opt := GetCommonOptions(&Options{}, opts...)

	ext := filepath.Ext(opt.URI)
	if uri, err := url.Parse(opt.URI); err == nil && uri.Host != "" && (uri.Scheme == "http" || uri.Scheme == "https") {
		ext = path.Ext(uri.Path)
	}

	parser, ok := p.parsers[ext]

	if !ok {
		parser = p.fallbackParser
	}

	if parser == nil {
		return nil, errors.New("no parser found for extension " + ext)
	}

	docs, err := parser.Parse(ctx, reader, opts...)
	if err != nil {
		return nil, err
	}

	for _, doc := range docs {
		if doc == nil {
			continue
		}

		if doc.MetaData == nil {
			doc.MetaData = make(map[string]any)
		}

		for k, v := range opt.ExtraMeta {
			doc.MetaData[k] = v
		}
	}

	return docs, nil
}
