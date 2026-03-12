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

	"github.com/cloudwego/eino/schema"
)

// Parser converts raw content from an [io.Reader] into [schema.Document] values.
//
// Parse may return multiple documents from a single reader (e.g. a PDF with
// per-page splitting). The reader is consumed during Parse and must not be
// reused.
//
// Parsers are typically not called directly — they are configured on a
// [document.Loader] and invoked via [document.WithParserOptions].
type Parser interface {
	Parse(ctx context.Context, reader io.Reader, opts ...Option) ([]*schema.Document, error)
}
