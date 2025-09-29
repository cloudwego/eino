package deep

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino-ext/components/tool/commandline"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

func newBuiltinTools(op commandline.Operator) ([]tool.BaseTool, error) {
	var ts []tool.BaseTool
	t, err := newWriteTodosTool()
	if err != nil {
		return nil, err
	}
	ts = append(ts, t)

	t, err = newWriteFileTool(op)
	if err != nil {
		return nil, err
	}
	ts = append(ts, t)

	t, err = newReadFileTool(op)
	if err != nil {
		return nil, err
	}
	ts = append(ts, t)

	t, err = newLSTool(op)
	if err != nil {
		return nil, err
	}
	ts = append(ts, t)

	return ts, nil
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

func newWriteFileTool(op commandline.Operator) (tool.InvokableTool, error) {
	return utils.InferTool("write_file", writeFileToolDescription, func(ctx context.Context, input writeFileArguments) (string, error) {
		err := op.WriteFile(ctx, input.FilePath, input.Content)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Updated file %s", input.FilePath), nil
	})
}

type readFileArguments struct {
	FilePath string `json:"filePath"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func newReadFileTool(op commandline.Operator) (tool.InvokableTool, error) {
	return utils.InferTool("read_file", readFileToolDescription, func(ctx context.Context, input readFileArguments) (output string, err error) {
		content, err := op.ReadFile(ctx, input.FilePath)
		if err != nil {
			return "", err
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

func newLSTool(op commandline.Operator) (tool.InvokableTool, error) {
	return utils.InferTool("ls", listFileToolDescription, func(ctx context.Context, input lsArguments) (string, error) {
		return op.RunCommand(ctx, "ls")
	})
}

// todo EditFile
