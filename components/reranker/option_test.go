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

package reranker

import (
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

func TestOptions(t *testing.T) {
	convey.Convey("test options", t, func() {
		var (
			topK           = 2
			scoreThreshold = 4.0
		)

		opts := GetCommonOptions(nil,
			WithTopK(topK),
			WithScoreThreshold(scoreThreshold),
		)

		convey.So(opts.TopK, convey.ShouldNotBeNil)
		convey.So(*opts.TopK, convey.ShouldEqual, topK)
		convey.So(opts.ScoreThreshold, convey.ShouldNotBeNil)
		convey.So(*opts.ScoreThreshold, convey.ShouldEqual, scoreThreshold)
	})
}
