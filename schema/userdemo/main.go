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
		case schema.BlockTypeAssistantGenText:
			for _, annotation := range block.AssistantGenText.Annotations {
				switch annotation_ := annotation.(type) {
				case schema.TextURLCitationAccessor:
					println(annotation_.GetTextURLCitation().Title)

					// If you want to get the raw annotation, you can do it like this:
					raw, ok := annotation_.(*openaidemo.AssistantGenTextAnnotation)
					if !ok {
						panic("not AssistantGenTextAnnotation")
					}

					fmt.Println(raw.URLCitation.URL)

				default:
					raw, ok := annotation_.(*openaidemo.AssistantGenTextAnnotation)
					if !ok {
						panic("not AssistantGenTextAnnotation")
					}

					fmt.Println(raw.ContainerFileCitation.ContainerID)
				}
			}

		case schema.BlockTypeProviderBuiltinToolResult:
			switch result := block.ProviderBuiltinToolResult.Result.(type) {
			case schema.WebSearchResultAccessor:
				fmt.Println(result.GetWebSearchResult().Sources)

				// If you want to get the raw result, you can do it like this:
				raw, ok := result.(*openaidemo.BuiltinToolResult)
				if !ok {
					panic("not BuiltinToolResult")
				}

				fmt.Println(raw.WebSearch.Sources)

			default:
				raw, ok := result.(*openaidemo.BuiltinToolResult)
				if !ok {
					panic("not BuiltinToolResult")
				}

				fmt.Println(raw.CodeExecution.Outputs)
			}
		}
	}
}
