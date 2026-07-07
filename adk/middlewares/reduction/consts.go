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

import (
	"fmt"

	"github.com/cloudwego/eino/adk/internal"
)

const (
	truncFmt = `<persisted-output>
Output too large ({original_size}). Full output saved to: {file_path}
Preview (first {preview_size}):
{preview_first}

Preview (last {preview_size}):
{preview_last}

</persisted-output>`
	truncFmtZh = `<persisted-output>
输出结果过大 ({original_size}). 完整输出保存到: {file_path}
预览 (前 {preview_size}):
{preview_first}

预览 (后 {preview_size}):
{preview_last}

</persisted-output>`
)

const (
	streamTruncFmt = `<persisted-output>
Output truncated after {preview_size} bytes were streamed. {offload_notify} {error_msg_notify}
</persisted-output>`
	streamTruncFmtZh = `<persisted-output>
输出结果在流式传输 {preview_size} 字节后被截断。{offload_notify} {error_msg_notify}
</persisted-output>`
)

const (
	clearWithOffloadingFmt = `<persisted-output>Tool result saved to: {file_path}
Use {read_tool_name} to view</persisted-output>`
	clearWithOffloadingFmtZh = `<persisted-output>工具结果已保存至: {file_path}
使用 {read_tool_name} 进行查看</persisted-output>`

	clearWithoutOffloadingFmt   = `[Old tool result content cleared]`
	clearWithoutOffloadingFmtZh = `[工具输出结果已清理]`
)

const (
	msgClearedFlag = "_reduction_mw_processed"
)

func getTruncFmt() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: truncFmt,
		Chinese: truncFmtZh,
	})
}

func getStreamTruncFmt() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: streamTruncFmt,
		Chinese: streamTruncFmtZh,
	})
}

func formatStreamOffloadSavedNotify(filePath string) string {
	return fmt.Sprintf(internal.SelectPrompt(internal.I18nPrompts{
		English: "Full output saved to: %s.",
		Chinese: "完整输出保存到: %s。",
	}), filePath)
}

func formatStreamOffloadFailedNotify(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(internal.SelectPrompt(internal.I18nPrompts{
		English: "Failed to save full output: %v.",
		Chinese: "完整输出保存失败: %v。",
	}), err)
}

func getClearWithOffloadingFmt() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: clearWithOffloadingFmt,
		Chinese: clearWithOffloadingFmtZh,
	})
}

func getClearWithoutOffloadingFmt() string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: clearWithoutOffloadingFmt,
		Chinese: clearWithoutOffloadingFmtZh,
	})
}

type scene int

const (
	sceneTruncation scene = 1
	sceneClear      scene = 2
)
