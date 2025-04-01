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

package react

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/flow/agent"
)

type myToolOptions struct {
	field1 string
}

func WithField1(field1 string) tool.Option {
	return tool.WrapImplSpecificOptFn(func(o *myToolOptions) {
		o.field1 = field1
	})
}

func TestGetComposeOptions(t *testing.T) {
	opt := WithToolOptions(WithField1("field1"))
	options := agent.GetComposeOptions(opt)
	assert.Equal(t, 1, len(options))
}
