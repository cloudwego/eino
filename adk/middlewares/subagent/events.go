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

package subagent

import (
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
)

// extractTextContent returns the plain-text content of a message. It delegates to
// internal.ExtractTextContent so the message-flattening logic has a single owner
// shared with the adk package.
func extractTextContent[M adk.MessageType](msg M) string {
	return internal.ExtractTextContent(msg)
}
