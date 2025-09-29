package deep

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

func newBuiltinTools() ([]tool.BaseTool, error) {
	var ts []tool.BaseTool
	t, err := newWriteTodosTool()
	if err != nil {
		return nil, err
	}
	ts = append(ts, t)

	//t, err = newWriteFileTool()
	//if err != nil {
	//	return nil, err
	//}
	//ts = append(ts, t)
	//
	//t, err = newReadFileTool()
	//if err != nil {
	//	return nil, err
	//}
	//ts = append(ts, t)
	//
	//t, err = newLSTool()
	//if err != nil {
	//	return nil, err
	//}
	//ts = append(ts, t)

	return ts, nil
}

type submitResultArguments struct {
	Result string   `json:"result" jsonschema:"description=A summary or detailed description of the task's final outcome."`
	Files  []string `json:"files" jsonschema:"description=A list of file paths directly related to the final result. Do not include temporary or intermediate files."`
}

type Result struct {
	Result string            `json:"result"`
	Files  map[string]string `json:"files"`
}

func newSubmitResultTool() (tool.BaseTool, error) {
	return utils.InferTool(
		"submit_result",
		"Submit the final result of a completed task, including any files directly related to the final outcome.",
		func(ctx context.Context, input submitResultArguments) (output string, err error) {
			allFiles := getFiles(ctx)
			files := make(map[string]string)
			for _, name := range input.Files {
				if f, ok := allFiles[name]; ok {
					files[name] = f
				}
			}
			result := &Result{
				Result: input.Result,
				Files:  files,
			}
			return sonic.MarshalString(result)
		})
}

type TODO struct {
	Content string `json:"content"`
	Status  string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed"`
}

type writeTodosArguments struct {
	Todos []TODO `json:"todos"`
}

func newWriteTodosTool() (tool.InvokableTool, error) {
	return utils.InferTool("write_todos", writeTodosToolDescription, func(ctx context.Context, input writeTodosArguments) (output string, err error) {
		adk.AddSessionValue(ctx, SessionKeyTodos, input.Todos)
		todos, err := sonic.MarshalString(input.Todos)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Updated todo list to %s", todos), nil
	})
}

type writeFileArguments struct {
	FilePath string `json:"filePath"`
	Content  string `json:"content"`
}

func newWriteFileTool() (tool.InvokableTool, error) {
	return utils.InferTool("write_file", writeFileToolDescription, func(ctx context.Context, input writeFileArguments) (string, error) {
		files := getFiles(ctx)
		files[input.FilePath] = input.Content
		adk.AddSessionValue(ctx, SessionKeyFiles, files)
		return fmt.Sprintf("Updated file %s", input.FilePath), nil
	})
}

type readFileArguments struct {
	FilePath string `json:"filePath"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func newReadFileTool() (tool.InvokableTool, error) {
	return utils.InferTool("read_file", readFileToolDescription, func(ctx context.Context, input readFileArguments) (output string, err error) {
		files := getFiles(ctx)
		content, ok := files[input.FilePath]
		if !ok {
			return "", fmt.Errorf("file not found: %s", input.FilePath)
		}
		lines := strings.Split(strings.TrimSpace(content), "\n")
		if input.Offset >= len(lines) {
			return "", fmt.Errorf("error: Line offset %d exceeds file length (%d lines)", input.Offset, len(lines))
		}
		endIdx := input.Offset + input.Limit
		if endIdx > len(lines) {
			endIdx = len(lines)
		}
		lines = lines[input.Offset:endIdx]
		return strings.Join(lines, "\n"), nil
	})
}

type lsArguments struct{}

func newLSTool() (tool.InvokableTool, error) {
	return utils.InferTool("ls", listFileToolDescription, func(ctx context.Context, input lsArguments) (string, error) {
		files := getFiles(ctx)
		sb := &strings.Builder{}
		sb.WriteString("[")
		for name, _ := range files {
			sb.WriteString(name)
			sb.WriteString(",")
		}
		sb.WriteString("]")
		return sb.String(), nil
	})
}

// todo EditFile

func getFiles(ctx context.Context) map[string]string {
	f, ok := adk.GetSessionValue(ctx, SessionKeyFiles)
	if ok {
		return f.(map[string]string)
	}
	return make(map[string]string)
}
