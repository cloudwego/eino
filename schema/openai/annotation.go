/*
 * Copyright 2025 CloudWeGo Authors
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

package openai

type TextAnnotation struct {
	Type TextAnnotationType `json:"type"`

	FileCitation          *TextAnnotationFileCitation          `json:"file_citation,omitempty"`
	URLCitation           *TextAnnotationURLCitation           `json:"url_citation,omitempty"`
	ContainerFileCitation *TextAnnotationContainerFileCitation `json:"container_file_citation,omitempty"`
	FilePath              *TextAnnotationFilePath              `json:"file_path,omitempty"`
}

type TextAnnotationFileCitation struct {
	// The ID of the file.
	FileID string `json:"file_id"`
	// The filename of the file cited.
	Filename string `json:"filename"`

	// The index of the file in the list of files.
	Index int64 `json:"index"`
}

type TextAnnotationURLCitation struct {
	// The title of the web resource.
	Title string `json:"title"`
	// The URL of the web resource.
	URL string `json:"url"`

	// The index of the first character of the URL citation in the message.
	StartIndex int64 `json:"start_index"`
	// The index of the last character of the URL citation in the message.
	EndIndex int64 `json:"end_index"`
}

type TextAnnotationContainerFileCitation struct {
	// The ID of the container file.
	ContainerID string `json:"container_id"`

	// The ID of the file.
	FileID string `json:"file_id"`
	// The filename of the container file cited.
	Filename string `json:"filename"`

	// The index of the first character of the container file citation in the message.
	StartIndex int64 `json:"start_index"`
	// The index of the last character of the container file citation in the message.
	EndIndex int64 `json:"end_index"`
}

type TextAnnotationFilePath struct {
	// The ID of the file.
	FileID string `json:"file_id"`

	// The index of the file in the list of files.
	Index int64 `json:"index"`
}
