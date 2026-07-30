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

package filesystem

import (
	"strings"
	"testing"

	"github.com/slongfield/pyfmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestToolDescPyfmtTemplates guards the tool-description constants that double as
// pyfmt templates. pyfmt treats every '{' / '}' as a format directive, so any
// literal brace added to one of these constants (a JSON example, a Go type like
// interface{}, etc.) that isn't escaped as {{ / }} makes pyfmt.Fmt fail at
// runtime — the tool then can't be constructed. This test runs pyfmt.Fmt on each
// templated constant with the exact key sets used at the real call sites (see
// newReadFileTool / newMultiModalReadFileTool) so such a regression is caught here
// instead of at agent construction time.
func TestToolDescPyfmtTemplates(t *testing.T) {
	cases := []struct {
		name     string
		tmpl     string
		keys     map[string]any
		wantSubs string // substring the resolved key must produce ("" = key resolves to empty)
	}{
		{
			name: "ReadFileToolDesc/standard",
			tmpl: ReadFileToolDesc,
			keys: map[string]any{"EnhancedReadFileDesc": ""},
		},
		{
			name:     "ReadFileToolDesc/multimodal",
			tmpl:     ReadFileToolDesc,
			keys:     map[string]any{"EnhancedReadFileDesc": EnhancedReadFileDesc},
			wantSubs: "Reads images",
		},
		{
			name: "ReadFileToolDescChinese/standard",
			tmpl: ReadFileToolDescChinese,
			keys: map[string]any{"EnhancedReadFileDesc": ""},
		},
		{
			name:     "ReadFileToolDescChinese/multimodal",
			tmpl:     ReadFileToolDescChinese,
			keys:     map[string]any{"EnhancedReadFileDesc": EnhancedReadFileDescChinese},
			wantSubs: "可读取图片",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pyfmt.Fmt(tc.tmpl, tc.keys)
			require.NoError(t, err, "pyfmt.Fmt must not fail — a literal brace in the constant likely needs escaping as {{ / }}")
			// The placeholder must be fully substituted, not left verbatim.
			assert.NotContains(t, got, "{EnhancedReadFileDesc}", "placeholder was not substituted")
			if tc.wantSubs != "" {
				assert.Contains(t, got, tc.wantSubs)
			}
		})
	}
}

// TestEnhancedReadFileDescNoBraces ensures the enhanced suffixes stay brace-free:
// they are substituted INTO a pyfmt template, so a literal brace in them would
// survive into the final string and be re-interpreted if the result were ever
// formatted again.
func TestEnhancedReadFileDescNoBraces(t *testing.T) {
	for name, s := range map[string]string{
		"EnhancedReadFileDesc":        EnhancedReadFileDesc,
		"EnhancedReadFileDescChinese": EnhancedReadFileDescChinese,
	} {
		assert.False(t, strings.ContainsAny(s, "{}"), "%s must not contain literal braces", name)
	}
}
