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

package adk

import "github.com/cloudwego/eino/adk/internal"

// Language represents the language setting for the ADK built-in prompts.
type Language = internal.Language

// I18nPrompts holds prompt strings for different languages.
type I18nPrompts = internal.I18nPrompts

const (
	// LanguageEnglish represents English language.
	LanguageEnglish Language = internal.LanguageEnglish
	// LanguageChinese represents Chinese language.
	LanguageChinese Language = internal.LanguageChinese
)

// SetLanguage sets the language for the ADK built-in prompts.
// The default language is English if not explicitly set.
func SetLanguage(lang Language) error {
	return internal.SetLanguage(lang)
}

// ReminderMessageRole represents the process-wide message role used for
// mid-conversation reminders (system-reminder messages) injected by ADK middlewares.
//
// SetReminderMessageRole changes the role for all agent instances and sessions in
// the process. It controls newly injected reminders. When the built-in reminder
// middlewares prepare model input, they also normalize existing reminders in that
// session's history to the current role. Changing the setting does not rewrite
// previously persisted session events.
//
// Reminder messages themselves persist within their originating session; a new
// session does not inherit another session's reminders.
type ReminderMessageRole = internal.ReminderMessageRole

const (
	// ReminderMessageRoleSystem injects reminders as system-role messages (default).
	ReminderMessageRoleSystem ReminderMessageRole = internal.ReminderMessageRoleSystem
	// ReminderMessageRoleUser injects reminders as user-role messages, for models that
	// reject non-leading system messages.
	ReminderMessageRoleUser ReminderMessageRole = internal.ReminderMessageRoleUser
)

// SetReminderMessageRole sets the process-wide role for mid-conversation reminders
// injected by ADK middlewares. It affects all agent instances and sessions.
// The default is ReminderMessageRoleSystem if not explicitly set.
func SetReminderMessageRole(r ReminderMessageRole) error {
	return internal.SetReminderMessageRole(r)
}

// SelectPrompt returns the prompt string for the current ADK built-in prompt language.
func SelectPrompt(prompts I18nPrompts) string {
	return internal.SelectPrompt(prompts)
}
