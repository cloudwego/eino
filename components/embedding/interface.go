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

package embedding

import "context"

// Embedder embeds input texts into vector representations.
//
//go:generate  mockgen -destination ../../internal/mock/components/embedding/Embedding_mock.go --package embedding -source interface.go
type Embedder interface {
	EmbedStrings(ctx context.Context, texts []string, opts ...Option) ([][]float64, error) // invoke
}
