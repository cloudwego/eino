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

package schema

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMediaFileID(t *testing.T) {
	const fileID = "provider-file-123"
	for _, format := range []struct {
		name     string
		typ      FormatType
		template string
	}{
		{"fstring", FString, "{file_id}"},
		{"go_template", GoTemplate, "{{.file_id}}"},
		{"jinja2", Jinja2, "{{file_id}}"},
	} {
		t.Run(format.name, func(t *testing.T) {
			common := MessagePartCommon{FileID: &format.template}
			msg := &Message{Role: User, UserInputMultiContent: []MessageInputPart{
				{Type: ChatMessagePartTypeImageURL, Image: &MessageInputImage{MessagePartCommon: common}},
				{Type: ChatMessagePartTypeAudioURL, Audio: &MessageInputAudio{MessagePartCommon: common}},
				{Type: ChatMessagePartTypeVideoURL, Video: &MessageInputVideo{MessagePartCommon: common}},
				{Type: ChatMessagePartTypeFileURL, File: &MessageInputFile{MessagePartCommon: common}},
			}}
			formatted, err := msg.Format(context.Background(), map[string]any{"file_id": fileID}, format.typ)
			require.NoError(t, err)
			parts := formatted[0].UserInputMultiContent
			for _, source := range []*string{parts[0].Image.FileID, parts[1].Audio.FileID, parts[2].Video.FileID, parts[3].File.FileID} {
				require.NotNil(t, source)
				require.Equal(t, fileID, *source)
			}
			require.Equal(t, format.template, *msg.UserInputMultiContent[0].Image.FileID)
			require.Contains(t, formatted[0].String(), "file_id="+fileID)
			data, err := json.Marshal(formatted[0])
			require.NoError(t, err)
			require.Contains(t, string(data), `"file_id":"`+fileID+`"`)
			var restored Message
			require.NoError(t, json.Unmarshal(data, &restored))
			require.Equal(t, formatted[0], &restored)

			agentic := &AgenticMessage{Role: AgenticRoleTypeUser, ContentBlocks: []*ContentBlock{
				NewContentBlock(&UserInputImage{FileID: format.template}),
				NewContentBlock(&UserInputAudio{FileID: format.template}),
				NewContentBlock(&UserInputVideo{FileID: format.template}),
				NewContentBlock(&UserInputFile{FileID: format.template}),
			}}
			agenticFormatted, err := agentic.Format(context.Background(), map[string]any{"file_id": fileID}, format.typ)
			require.NoError(t, err)
			blocks := agenticFormatted[0].ContentBlocks
			for _, source := range []string{blocks[0].UserInputImage.FileID, blocks[1].UserInputAudio.FileID, blocks[2].UserInputVideo.FileID, blocks[3].UserInputFile.FileID} {
				require.Equal(t, fileID, source)
			}
			require.Equal(t, format.template, agentic.ContentBlocks[0].UserInputImage.FileID)
			require.Contains(t, agenticFormatted[0].String(), "file_id: "+fileID)
			data, err = json.Marshal(agenticFormatted[0])
			require.NoError(t, err)
			var restoredAgentic AgenticMessage
			require.NoError(t, json.Unmarshal(data, &restoredAgentic))
			require.Equal(t, agenticFormatted[0], &restoredAgentic)
		})
	}
}

func TestToolResultFileID(t *testing.T) {
	fileID := "provider-file-123"
	common := MessagePartCommon{FileID: &fileID}
	result := &ToolResult{Parts: []ToolOutputPart{
		{Type: ToolPartTypeImage, Image: &ToolOutputImage{MessagePartCommon: common}},
		{Type: ToolPartTypeAudio, Audio: &ToolOutputAudio{MessagePartCommon: common}},
		{Type: ToolPartTypeVideo, Video: &ToolOutputVideo{MessagePartCommon: common}},
		{Type: ToolPartTypeFile, File: &ToolOutputFile{MessagePartCommon: common}},
	}}
	data, err := json.Marshal(result)
	require.NoError(t, err)
	var restored ToolResult
	require.NoError(t, json.Unmarshal(data, &restored))
	parts, err := restored.ToMessageInputParts()
	require.NoError(t, err)
	require.Len(t, parts, 4)
	for _, source := range []*string{parts[0].Image.FileID, parts[1].Audio.FileID, parts[2].Video.FileID, parts[3].File.FileID} {
		require.NotNil(t, source)
		require.Equal(t, fileID, *source)
	}
}
