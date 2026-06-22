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

package team

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateName_Valid(t *testing.T) {
	valid := []string{
		"a",
		"A",
		"0",
		"agent",
		"worker-1",
		"worker_1",
		"v1.2",
		"Researcher.Bot-2",
		strings.Repeat("a", maxNameLength),
	}
	for _, name := range valid {
		assert.NoError(t, validateName("name", name), "expected %q to be valid", name)
	}
}

func TestValidateName_Invalid(t *testing.T) {
	invalid := []string{
		"",                                   // empty
		".",                                  // current dir
		"..",                                 // parent dir
		"../escape",                          // traversal
		"a/b",                                // path separator
		"a\\b",                               // windows separator
		"-leading",                           // leading dash
		".hidden",                            // leading dot
		"_underscore",                        // leading underscore
		"has space",                          // whitespace
		"tab\there",                          // tab
		"new\nline",                          // newline
		"*",                                  // broadcast wildcard
		"wild*card",                          // embedded wildcard
		"name@team",                          // '@' not allowed
		"emoji😀",                             // non-ascii
		strings.Repeat("a", maxNameLength+1), // too long
	}
	for _, name := range invalid {
		assert.Error(t, validateName("name", name), "expected %q to be invalid", name)
	}
}

func TestValidateMemberName_RejectsLeader(t *testing.T) {
	err := validateMemberName(LeaderAgentName)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reserved for the team leader")
}

func TestValidateMemberName_AllowsRegular(t *testing.T) {
	assert.NoError(t, validateMemberName("researcher"))
	assert.NoError(t, validateMemberName("agent"))
}

func TestValidateTeamName_Wildcard(t *testing.T) {
	err := validateTeamName("team*")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "broadcast")
}

func TestSuffixedMemberName_ShortBase(t *testing.T) {
	// A short base name is suffixed verbatim and stays valid.
	got := suffixedMemberName("worker", 2)
	assert.Equal(t, "worker-2", got)
	assert.NoError(t, validateMemberName(got))
}

func TestSuffixedMemberName_TruncatesNearLimit(t *testing.T) {
	base := strings.Repeat("a", maxNameLength)
	for i := 2; i <= 1000; i++ {
		got := suffixedMemberName(base, i)
		assert.LessOrEqual(t, len(got), maxNameLength,
			"suffixed name %q exceeds maxNameLength", got)
		assert.NoError(t, validateMemberName(got),
			"suffixed name %q must remain valid", got)
		assert.True(t, strings.HasSuffix(got, fmt.Sprintf("-%d", i)))
	}
}

func TestSuffixedMemberName_TrimsTrailingBodyChars(t *testing.T) {
	// Truncation that lands on a body-only char ('.', '_', '-') must trim it so
	// the result never contains sequences like "name.-2".
	base := strings.Repeat("a", maxNameLength-2) + ".."
	got := suffixedMemberName(base, 5)
	assert.NoError(t, validateMemberName(got))
	assert.False(t, strings.Contains(got, ".-"))
}
