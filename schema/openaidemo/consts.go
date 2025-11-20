package openaidemo

type ServerToolType string

const (
	ServerToolWebSearch ServerToolType = "web_search" // 引用 openai sdk 枚举定义
)

type WebSearchStatus string

const (
	WebSearchStatusInProgress WebSearchStatus = "in_progress" // 引用 openai sdk 枚举定义
	WebSearchStatusSearching  WebSearchStatus = "searching"   // 引用 openai sdk 枚举定义
	WebSearchStatusCompleted  WebSearchStatus = "completed"   // 引用 openai sdk 枚举定义
	WebSearchStatusFailed     WebSearchStatus = "failed"      // 引用 openai sdk 枚举定义
)

type WebSearchAction string

const (
	WebSearchActionSearch   WebSearchAction = "search"    // 引用 openai sdk 枚举定义
	WebSearchActionOpenPage WebSearchAction = "open_page" // 引用 openai sdk 枚举定义
	WebSearchActionFind     WebSearchAction = "find"      // 引用 openai sdk 枚举定义
)
