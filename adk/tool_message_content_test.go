package adk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// A tool that returns only text must still reach the model as text: the OpenAI Chat Completions adapter serialises a
// tool message from Content, and an empty Content is what made models answer "the screenshot is empty" while the file
// was on disk. A result with an image keeps Content empty (go-openai refuses Content and MultiContent together) and
// travels as parts instead.
func TestEnhancedInvokeEventFillsContentForATextOnlyResult(t *testing.T) {
	t.Run("text only", func(t *testing.T) {
		result := &schema.ToolResult{Parts: []schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: "Screenshot 100x50 pixels."},
			{Type: schema.ToolPartTypeText, Text: "Screenshot file: C:\\tmp\\shot.png"},
		}}
		event, err := typedToolEnhancedInvokeEvent[*schema.Message]("call_1", "computer_use", "msg_1", result)
		require.NoError(t, err)
		require.NotNil(t, event)
		msg := event.Output.MessageOutput.Message
		require.NotNil(t, msg)
		assert.Equal(t, schema.Tool, msg.Role)
		assert.Equal(t, "Screenshot 100x50 pixels.\nScreenshot file: C:\\tmp\\shot.png", msg.Content)
		require.Len(t, msg.UserInputMultiContent, 2)
	})

	t.Run("with an image", func(t *testing.T) {
		b64 := "AAAA"
		result := &schema.ToolResult{Parts: []schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: "Screenshot 100x50 pixels."},
			{Type: schema.ToolPartTypeImage, Image: &schema.ToolOutputImage{
				MessagePartCommon: schema.MessagePartCommon{Base64Data: &b64, MIMEType: "image/png"},
			}},
		}}
		event, err := typedToolEnhancedInvokeEvent[*schema.Message]("call_2", "computer_use", "msg_2", result)
		require.NoError(t, err)
		msg := event.Output.MessageOutput.Message
		require.NotNil(t, msg)
		assert.Empty(t, msg.Content, "a message with parts must not also carry Content; go-openai refuses that")
		require.Len(t, msg.UserInputMultiContent, 2)
		assert.Equal(t, schema.ChatMessagePartTypeImageURL, msg.UserInputMultiContent[1].Type)
	})
}
