/*
 * Copyright 2024 CloudWeGo Authors
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

package indexer

import (
	"testing"

	"github.com/smartystreets/goconvey/convey"

	"github.com/cloudwego/eino/internal/mock/components/embedding"
)

func TestOptions(t *testing.T) {
	convey.Convey("test options", t, func() {
		var (
			index      = "index"
			subIndexes = []string{"index_1", "index_2"}
			e          = &embedding.MockEmbedder{}
		)

		opts := GetCommonOptions(
			&Options{},
			WithIndex(index),
			WithSubIndexes(subIndexes),
			WithEmbedding(e),
		)

		convey.So(opts, convey.ShouldResemble, &Options{
			Index:      &index,
			SubIndexes: subIndexes,
			Embedding:  e,
		})
	})
}
