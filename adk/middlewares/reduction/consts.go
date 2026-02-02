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

package reduction

const (
	lineTruncFmt    = `... (line truncated due to length limitation, %d chars total)`
	contentTruncFmt = `... (content truncated due to length limitation, %d chars total)`
)

const (
	toolOffloadResultFmt = `Tool result is too large, retrieve from %s if needed`
)

const (
	msgReducedFlag   = "_reduction_mw_processed"
	msgReducedTokens = "_reduction_mw_tokens"
)
