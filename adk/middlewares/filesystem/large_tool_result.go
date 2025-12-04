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

package filesystem

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type toolResultOffloadingConfig struct {
	FS            Backend
	TokenLimit    int
	PathGenerator func(ctx context.Context, input *compose.ToolInput) (string, error)
}

func newToolResultOffloading(ctx context.Context, config *toolResultOffloadingConfig) compose.ToolMiddleware {
	offloading := &toolResultOffloading{
		fs:            config.FS,
		tokenLimit:    config.TokenLimit,
		pathGenerator: config.PathGenerator,
	}

	if offloading.tokenLimit == 0 {
		offloading.tokenLimit = 20000
	}

	if offloading.pathGenerator == nil {
		offloading.pathGenerator = func(ctx context.Context, input *compose.ToolInput) (string, error) {
			return fmt.Sprintf("/large_tool_result/%s", input.CallID), nil
		}
	}

	return compose.ToolMiddleware{
		Invokable:  offloading.invoke,
		Streamable: offloading.stream,
	}
}

type toolResultOffloading struct {
	fs            Backend
	tokenLimit    int
	pathGenerator func(ctx context.Context, input *compose.ToolInput) (string, error)
}

func (t *toolResultOffloading) invoke(endpoint compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		output, err := endpoint(ctx, input)
		if err != nil {
			return nil, err
		}
		result, err := t.handleResult(ctx, output.Result, input)
		if err != nil {
			return nil, err
		}
		return &compose.ToolOutput{Result: result}, nil
	}
}

func (t *toolResultOffloading) stream(endpoint compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		output, err := endpoint(ctx, input)
		if err != nil {
			return nil, err
		}
		result, err := concatString(output.Result)
		if err != nil {
			return nil, err
		}
		result, err = t.handleResult(ctx, result, input)
		if err != nil {
			return nil, err
		}
		return &compose.StreamToolOutput{Result: schema.StreamReaderFromArray([]string{result})}, nil
	}
}

func (t *toolResultOffloading) handleResult(ctx context.Context, result string, input *compose.ToolInput) (string, error) {
	if len(result) > t.tokenLimit*4 {
		path, err := t.pathGenerator(ctx, input)
		if err != nil {
			return "", err
		}

		nResult := formatToolMessage(result)
		nResult, err = pyfmt.Fmt(tooLargeToolMessage, map[string]any{
			"tool_call_id":   input.CallID,
			"file_path":      path,
			"content_sample": nResult,
		})
		if err != nil {
			return "", err
		}

		err = t.fs.Write(ctx, &WriteRequest{
			FilePath: path,
			Content:  result,
		})
		if err != nil {
			return "", err
		}

		return nResult, nil
	}

	return result, nil
}

func concatString(sr *schema.StreamReader[string]) (string, error) {
	if sr == nil {
		return "", errors.New("stream is nil")
	}
	sb := strings.Builder{}
	for {
		str, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return sb.String(), nil
		}
		if err != nil {
			return "", err
		}
		sb.WriteString(str)
	}
}

func formatToolMessage(s string) string {
	reader := bufio.NewScanner(strings.NewReader(s))
	var b strings.Builder

	lineNum := 1
	for reader.Scan() {
		if lineNum > 10 {
			break
		}
		line := reader.Text()

		if utf8.RuneCountInString(line) > 1000 {
			runes := []rune(line)
			line = string(runes[:1000])
		}

		b.WriteString(fmt.Sprintf("%d: %s\n", lineNum, line))

		lineNum++
	}

	return b.String()
}
