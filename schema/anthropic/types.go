package anthropic

type TextCitationType string

const (
	TextCitationTypeCharLocation            TextCitationType = "char_location"
	TextCitationTypePageLocation            TextCitationType = "page_location"
	TextCitationTypeContentBlockLocation    TextCitationType = "content_block_location"
	TextCitationTypeWebSearchResultLocation TextCitationType = "web_search_result_location"
)
