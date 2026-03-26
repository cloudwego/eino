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

// util.go provides low-level helpers: panic-safe goroutines, error joining,
// and tool result serialisation.

package team

import (
	"errors"
	"runtime/debug"
	"strings"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/internal"
)

// selectToolDesc selects the appropriate tool description based on locale.
func selectToolDesc(english, chinese string) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: english,
		Chinese: chinese,
	})
}

// safeGoWithLogger runs f in a new goroutine, recovering from panics and logging to logger.
func safeGoWithLogger(logger Logger, f func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Printf("safeGo panic: %v\n%s", r, debug.Stack())
			}
		}()
		f()
	}()
}

// marshalToolResult serializes a map to a JSON string for tool return values.
// On serialization failure, returns a minimal JSON object with the error.
func marshalToolResult(data map[string]any) string {
	result, err := sonic.MarshalString(data)
	if err != nil {
		// Use sonic to marshal the error string so that special characters
		// (quotes, backslashes) are properly escaped in the JSON output.
		errJSON, _ := sonic.MarshalString(map[string]string{"error": err.Error()})
		if errJSON == "" {
			errJSON = `{"error":"marshal failed"}`
		}
		return errJSON
	}
	return result
}

// joinErrors combines multiple errors into a single error.
// Returns nil if no non-nil errors are provided.
//
// NOTE: multiError.Unwrap() []error requires Go 1.20+ to be recognized by
// errors.Is/errors.As. Under Go 1.18/1.19, only Error() is usable. This is
// acceptable because callers currently only log or return the combined error
// without unwrapping individual sub-errors.
func joinErrors(errs ...error) error {
	var nonNil []error
	for _, e := range errs {
		if e != nil {
			nonNil = append(nonNil, e)
		}
	}
	if len(nonNil) == 0 {
		return nil
	}
	return &multiError{errs: nonNil}
}

type multiError struct {
	errs []error
}

func (me *multiError) Error() string {
	msgs := make([]string, len(me.errs))
	for i, e := range me.errs {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "; ")
}

// Unwrap returns the list of wrapped errors for use with errors.Is/errors.As (Go 1.20+).
func (me *multiError) Unwrap() []error {
	return me.errs
}

// Is supports errors.Is on Go 1.19 where multi-unwrap is not recognized.
func (me *multiError) Is(target error) bool {
	for _, e := range me.errs {
		if errors.Is(e, target) {
			return true
		}
	}
	return false
}

// As supports errors.As on Go 1.19 where multi-unwrap is not recognized.
func (me *multiError) As(target any) bool {
	for _, e := range me.errs {
		if errors.As(e, target) {
			return true
		}
	}
	return false
}
