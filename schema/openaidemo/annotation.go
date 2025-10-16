package openaidemo

import (
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[AssistantGenTextAnnotation]("_eino_ext_openai_assistant_gen_text_annotations")
}

func (a *AssistantGenTextAnnotation) ImplAssistantGenTextAnnotation() {}

type AssistantGenTextAnnotation struct {
	Annotations []*Citation
}

type CitationType string

const (
	CitationTypeURL CitationType = "url_citation"
)

type Citation struct {
	Type        CitationType `json:"type,required"`
	URLCitation *AnnotationURLCitation
}

type AnnotationURLCitation struct {
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
