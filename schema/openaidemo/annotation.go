package openaidemo

import (
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[AssistantGenTextAnnotation]("_eino_ext_openai_assistant_gen_text_annotations")
}

type AssistantGenTextAnnotation struct {
	URLCitation           *ResponseOutputTextAnnotationURLCitation
	ContainerFileCitation *ResponseOutputTextAnnotationContainerFileCitation
}

const provider = "OpenAI"

func (a *AssistantGenTextAnnotation) AnnotationProvider() string {
	return provider
}

func (a *AssistantGenTextAnnotation) GetTextURLCitation() *schema.TextURLCitation {
	if a == nil || a.URLCitation == nil {
		return nil
	}

	return &schema.TextURLCitation{
		URL:        a.URLCitation.URL,
		Title:      a.URLCitation.Title,
		StartIndex: &a.URLCitation.StartIndex,
		EndIndex:   &a.URLCitation.EndIndex,
	}
}

type ResponseOutputTextAnnotationURLCitation struct {
	// The index of the last character of the URL citation in the message.
	EndIndex int64 `json:"end_index,required"`
	// The index of the first character of the URL citation in the message.
	StartIndex int64 `json:"start_index,required"`
	// The title of the web resource.
	Title string `json:"title,required"`
	// The type of the URL citation. Always `url_citation`.
	Type string `json:"type,required"`
	// The URL of the web resource.
	URL string `json:"url,required"`
}

type ResponseOutputTextAnnotationContainerFileCitation struct {
	// The ID of the container file.
	ContainerID string `json:"container_id,required"`
	// The index of the last character of the container file citation in the message.
	EndIndex int64 `json:"end_index,required"`
	// The ID of the file.
	FileID string `json:"file_id,required"`
	// The filename of the container file cited.
	Filename string `json:"filename,required"`
	// The index of the first character of the container file citation in the message.
	StartIndex int64 `json:"start_index,required"`
	// The type of the container file citation. Always `container_file_citation`.
	Type string `json:"type,required"`
}
