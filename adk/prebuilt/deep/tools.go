package deep

import "github.com/cloudwego/eino/components/tool"

var builtinTools = []tool.BaseTool{newWriteFileTool()}

type writeFileArguments struct {
	FilePath string
	Content  string
}

func newWriteFileTool() tool.BaseTool {
	return nil
}
