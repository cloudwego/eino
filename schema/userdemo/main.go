package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/schema/openaidemo"
)

func main() {
	cm := &openaidemo.AgenticModel{}

	output, err := cm.Stream(context.Background(), nil)
	if err != nil {
		panic(err)
	}

	msgs := []*schema.AgenticMessage{}
	for {
		msg, err := output.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			panic(err)
		}

		msgs = append(msgs, msg)
	}

	if len(msgs) == 0 {
		panic("no message")
	}

	msg, err := schema.ConcatAgenticMessages(msgs)
	if err != nil {
		panic(err)
	}

	for _, block := range msg.Blocks {
		switch block.Type {
		case schema.ContentBlockTypeAssistantGenText:
			annotation := block.AssistantGenText.Annotation.(*openaidemo.AssistantGenTextAnnotation)

			for _, citation := range annotation.Annotations {
				switch citation.Type {
				case openaidemo.CitationTypeURL:
					fmt.Println(citation.URLCitation.URL)
				default:
					panic("unknown citation type " + citation.Type)
				}
			}

		case schema.ContentBlockTypeServerToolCall:
			args := block.ServerToolCall.Arguments
			args_ := args.(*openaidemo.ServerToolCall)

			switch block.ServerToolCall.Name {
			case string(openaidemo.ServerToolWebSearch):
				switch args_.WebSearch.ActionType {
				case "search":
					fmt.Println(args_.WebSearch.Search.Query)
				case "open_page":
					fmt.Println(args_.WebSearch.OpenPage.URL)
				case "find":
					fmt.Println(args_.WebSearch.Find.URL)
				default:
					panic("unknown web search type " + args_.WebSearch.ActionType)
				}

			default:
				panic("unknown server tool name " + block.ServerToolCall.Name)
			}

		case schema.ContentBlockTypeServerToolResult:
			result := block.ServerToolResult.Result
			result_ := result.(*openaidemo.ServerToolResult)

			switch block.ServerToolResult.Name {
			case string(openaidemo.ServerToolWebSearch):
				fmt.Println(result_.WebSearch.Sources)
			default:
				panic("unknown server tool name" + block.ServerToolResult.Name)
			}
		}
	}
}
