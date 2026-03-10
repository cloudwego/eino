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

package document

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Source identifies the external location of a document.
// URI can be a local file path or a remote URL reachable by the loader.
type Source struct {
	URI string
}

//go:generate  mockgen -destination ../../internal/mock/components/document/document_mock.go --package document -source interface.go

// Loader reads raw content from an external source and returns it as a slice
// of [schema.Document] values.
//
// The Source.URI may be a local file path or a remote URL. The loader is
// responsible for fetching the raw bytes; actual format parsing is typically
// delegated to a [parser.Parser] configured on the loader via
// [WithParserOptions].
//
// Document metadata ([schema.Document].MetaData) should be populated with at
// least the source URI so that downstream nodes can trace document provenance.
type Loader interface {
	Load(ctx context.Context, src Source, opts ...LoaderOption) ([]*schema.Document, error)
}

// Transformer converts a slice of [schema.Document] values into another slice,
// applying operations such as splitting, filtering, merging, or re-ranking.
//
// Implementations should preserve existing MetaData keys and merge rather than
// replace when adding their own metadata. Downstream nodes (e.g. Indexer,
// Retriever) may depend on metadata set by earlier pipeline stages.
type Transformer interface {
	Transform(ctx context.Context, src []*schema.Document, opts ...TransformerOption) ([]*schema.Document, error)
}
