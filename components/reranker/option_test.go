/*
 * Copyright 2024 CloudWeGo Authors
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
