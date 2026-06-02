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

package adk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptLanguageWrappers(t *testing.T) {
	require.NoError(t, SetLanguage(LanguageEnglish))
	t.Cleanup(func() {
		require.NoError(t, SetLanguage(LanguageEnglish))
	})

	prompts := I18nPrompts{
		English: "hello",
		Chinese: "你好",
	}

	assert.Equal(t, "hello", SelectPrompt(prompts))

	require.NoError(t, SetLanguage(LanguageChinese))
	assert.Equal(t, "你好", SelectPrompt(prompts))

	err := SetLanguage(Language(255))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid language")
}
