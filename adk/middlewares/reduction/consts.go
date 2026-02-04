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

// Package reduction provides middlewares to trim context and clear tool results.
package reduction

import "github.com/cloudwego/eino/adk/internal"

func getLineTruncFmt() string {
	s, _ := internal.SelectPrompt(internal.I18nPrompts{
		English: lineTruncFmt,
		Chinese: lineTruncFmtZh,
	})
	if s == "" {
		return lineTruncFmt
	}
	return s
}

func getContentTruncFmt() string {
	s, _ := internal.SelectPrompt(internal.I18nPrompts{
		English: contentTruncFmt,
		Chinese: contentTruncFmtZh,
	})
	if s == "" {
		return contentTruncFmt
	}
	return s
}

func getToolOffloadResultFmt() string {
	s, _ := internal.SelectPrompt(internal.I18nPrompts{
		English: toolOffloadResultFmt,
		Chinese: toolOffloadResultFmtZh,
	})
	if s == "" {
		return toolOffloadResultFmt
	}
	return s
}

const (
	lineTruncFmt   = `... (line truncated due to length limitation, %d chars total)`
	lineTruncFmtZh = `...(由于长度限制截断本行, 总计 %d 字符)`

	contentTruncFmt   = `... (content truncated due to length limitation, %d chars total)`
	contentTruncFmtZh = `...(由于长度限制截断末尾, 总计 %d 字符)`
)

const (
	toolOffloadResultFmt   = `Tool result is too large, retrieve from %s if needed`
	toolOffloadResultFmtZh = `工具输出结果过长, 需要时从 %s 中导入`
)

const (
	msgReducedFlag   = "_reduction_mw_processed"
	msgReducedTokens = "_reduction_mw_tokens"
)
