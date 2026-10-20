/*
 * Copyright 2026 CloudWeGo Authors
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

package modeltest_test

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model/modeltest"
	"github.com/cloudwego/eino/schema"
)

func Example() {
	m := modeltest.New().
		OnTurn(0).Reply("hello").
		OnTurn(1).ReplyToolCall("get_weather", `{"city":"sh"}`).
		Build()

	reply, _ := m.Generate(context.Background(), []*schema.Message{
		schema.UserMessage("hi"),
	})
	fmt.Println(reply.Content)
	fmt.Println(m.Calls()[0].LastUserMessage())

	// Output:
	// hello
	// hi
}
