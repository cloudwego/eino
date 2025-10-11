package deep

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

func newBuiltinTools() ([]tool.InvokableTool, error) {
	var ts []tool.InvokableTool
	t, err := newWriteTodosTool()
	if err != nil {
		return nil, err
	}
	ts = append(ts, t)

	return ts, nil
}

type submitResultArguments struct {
	Result string   `json:"result" jsonschema:"description=A summary or detailed description of the task's final outcome."`
	Files  []string `json:"files" jsonschema:"description=A list of file paths directly related to the final result. Do not include temporary or intermediate files."`
}

type Result struct {
	Result string   `json:"result"`
	Files  []string `json:"files"`
}

func newSubmitResultTool() (tool.InvokableTool, error) {
	t, err := utils.InferTool(
		"submit_result",
		"Submit the final result of a completed task, including any files directly related to the final outcome.",
		func(ctx context.Context, input submitResultArguments) (output string, err error) {
			result := &Result{
				Result: input.Result,
				Files:  input.Files,
			}
			return sonic.MarshalString(result)
		})
	if err != nil {
		return nil, err
	}
	return t, nil
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
