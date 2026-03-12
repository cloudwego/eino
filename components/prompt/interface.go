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

package prompt

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

var _ ChatTemplate = &DefaultChatTemplate{}

// ChatTemplate formats a variables map into a list of messages for a ChatModel.
//
// Format substitutes the values from vs into the template's message list and
// returns the resulting []*schema.Message. The exact substitution syntax
// (FString, GoTemplate, Jinja2) is determined at construction time.
//
// Variable keys present in the template but absent from vs produce a runtime
// error — there is no compile-time safety. Prefer consistent variable naming
// across templates and callers.
//
// In a Graph or Chain, ChatTemplate typically precedes ChatModel. Use
// compose.WithOutputKey to convert the prior node's output into the map[string]any
// that Format expects.
//
// See [FromMessages] and [schema.MessagesPlaceholder] for construction helpers.
type ChatTemplate interface {
	Format(ctx context.Context, vs map[string]any, opts ...Option) ([]*schema.Message, error)
}
