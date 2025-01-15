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

// Source is a document source.
// e.g. https://www.bytedance.com/docx/xxxx, https://xxx.xxx.xxx/xx.pdf.
// make sure the URI can be reached by service.
type Source struct {
	URI string
}

//go:generate  mockgen -destination ../../internal/mock/components/document/document_mock.go --package document -source interface.go

// Loader is a document loader.
type Loader interface {
	Load(ctx context.Context, src Source, opts ...LoaderOption) ([]*schema.Document, error)
}

// Transformer is to convert documents, such as split or filter.
type Transformer interface {
	Transform(ctx context.Context, src []*schema.Document, opts ...TransformerOption) ([]*schema.Document, error)
}
