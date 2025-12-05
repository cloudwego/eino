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

type TextAnnotationType string

const (
	TextAnnotationTypeFileCitation          TextAnnotationType = "file_citation"
	TextAnnotationTypeURLCitation           TextAnnotationType = "url_citation"
	TextAnnotationTypeContainerFileCitation TextAnnotationType = "container_file_citation"
	TextAnnotationTypeFilePath              TextAnnotationType = "file_path"
)
