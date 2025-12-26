package anthropic

type TextCitation struct {
	Type TextCitationType `json:"type"`

	CharLocation            *CitationCharLocation            `json:"char_location,omitempty"`
	PageLocation            *CitationPageLocation            `json:"page_location,omitempty"`
	ContentBlockLocation    *CitationContentBlockLocation    `json:"content_block_location,omitempty"`
	WebSearchResultLocation *CitationWebSearchResultLocation `json:"web_search_result_location,omitempty"`
}

type CitationCharLocation struct {
	CitedText string `json:"cited_text"`

	DocumentTitle string `json:"document_title"`
	DocumentIndex int64  `json:"document_index"`

	StartCharIndex int64 `json:"start_char_index"`
	EndCharIndex   int64 `json:"end_char_index"`
}

type CitationPageLocation struct {
	CitedText string `json:"cited_text"`

	DocumentTitle string `json:"document_title"`
	DocumentIndex int64  `json:"document_index"`

	StartPageNumber int64 `json:"start_page_number"`
	EndPageNumber   int64 `json:"end_page_number"`
}

type CitationContentBlockLocation struct {
	CitedText string `json:"cited_text"`

	DocumentTitle string `json:"document_title"`
	DocumentIndex int64  `json:"document_index"`

	StartBlockIndex int64 `json:"start_block_index"`
	EndBlockIndex   int64 `json:"end_block_index"`
}

type CitationWebSearchResultLocation struct {
	CitedText string `json:"cited_text"`

	Title string `json:"title"`
	URL   string `json:"url"`

	EncryptedIndex string `json:"encrypted_index"`
}
