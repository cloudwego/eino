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

// Package internal provides adk internal utils.
package internal

import (
	"fmt"
	"sync/atomic"
)

// Language represents the language setting for the ADK built-in prompts.
type Language uint8

const (
	// LanguageEnglish represents English language.
	LanguageEnglish Language = iota
	// LanguageChinese represents Chinese language.
	LanguageChinese
)

var language atomic.Value

// SetLanguage sets the language for the ADK built-in prompts.
// The default language is English if not explicitly set.
func SetLanguage(lang Language) {
	language.Store(lang)
}

// GetLanguage returns the current language setting for the ADK built-in prompts.
// Returns LanguageEnglish if no language has been set.
func getLanguage() Language {
	if l, ok := language.Load().(Language); ok {
		return l
	}
	return LanguageEnglish
}

// I18nPrompts holds prompt strings for different languages.
type I18nPrompts struct {
	English string
	Chinese string
}

// SelectPrompt returns the appropriate prompt string based on the current language setting.
// Returns an error if the current language is not supported.
func SelectPrompt(prompts I18nPrompts) (string, error) {
	lang := getLanguage()
	switch lang {
	case LanguageEnglish:
		return prompts.English, nil
	case LanguageChinese:
		return prompts.Chinese, nil
	default:
		return "", fmt.Errorf("failed to select prompts: unsupported language type: %d", lang)
	}
}
