package openai

type TextAnnotationType string

const (
	TextAnnotationTypeFileCitation          TextAnnotationType = "file_citation"
	TextAnnotationTypeURLCitation           TextAnnotationType = "url_citation"
	TextAnnotationTypeContainerFileCitation TextAnnotationType = "container_file_citation"
	TextAnnotationTypeFilePath              TextAnnotationType = "file_path"
)
