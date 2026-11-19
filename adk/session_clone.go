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
	"github.com/cloudwego/eino/internal/gmap"
	internalschema "github.com/cloudwego/eino/internal/schema"
)

// cloneSessionEvent copies a non-nil event and its payloads. The Extra map is
// copied. Its values, InterruptContext.Info and Extension.Data retain their
// references. Callers must keep the source stable during cloning and shared
// data read-only after publication.
func cloneSessionEvent[M MessageType](event *SessionEvent[M]) *SessionEvent[M] {
	cloned := *event
	// The map is independent; values are assigned as-is and retain their references.
	cloned.Extra = gmap.Clone(event.Extra)
	cloned.Message = internalschema.CloneMessage(event.Message)
	if event.MessageStreamIncomplete != nil {
		cloned.MessageStreamIncomplete = cloneSessionValue(event.MessageStreamIncomplete)
		cloned.MessageStreamIncomplete.Message = internalschema.CloneMessage(event.MessageStreamIncomplete.Message)
	}
	if event.MessagesReplaced != nil {
		messages := internalschema.CloneMessages(*event.MessagesReplaced)
		cloned.MessagesReplaced = &messages
	}
	if event.MessageUpdated != nil {
		cloned.MessageUpdated = cloneSessionValue(event.MessageUpdated)
		cloned.MessageUpdated.Message = internalschema.CloneMessage(event.MessageUpdated.Message)
	}
	if event.MessageInserted != nil {
		cloned.MessageInserted = cloneSessionValue(event.MessageInserted)
		cloned.MessageInserted.Message = internalschema.CloneMessage(event.MessageInserted.Message)
	}
	if event.MessagesDeleted != nil {
		cloned.MessagesDeleted = cloneSessionValue(event.MessagesDeleted)
		cloned.MessagesDeleted.MessageIDs = cloneSlice(event.MessagesDeleted.MessageIDs)
	}
	cloned.Rollback = cloneSessionValue(event.Rollback)
	if event.Lifecycle != nil {
		cloned.Lifecycle = cloneSessionValue(event.Lifecycle)
		cloned.Lifecycle.StopReason = cloneSessionValue(event.Lifecycle.StopReason)
	}
	if event.Error != nil {
		cloned.Error = cloneSessionValue(event.Error)
		cloned.Error.RetryStatus = cloneSessionValue(event.Error.RetryStatus)
	}
	if event.Span != nil {
		cloned.Span = cloneSessionValue(event.Span)
		cloned.Span.Tool = cloneSessionValue(event.Span.Tool)
		if event.Span.Model != nil {
			cloned.Span.Model = cloneSessionValue(event.Span.Model)
			cloned.Span.Model.Usage = cloneSessionValue(event.Span.Model.Usage)
			cloned.Span.Model.Timeout = cloneSessionValue(event.Span.Model.Timeout)
		}
	}
	cloned.Cancel = cloneSessionValue(event.Cancel)
	if event.Interrupt != nil {
		cloned.Interrupt = cloneSessionValue(event.Interrupt)
		if event.Interrupt.Contexts != nil {
			cloned.Interrupt.Contexts = make([]*InterruptContext, len(event.Interrupt.Contexts))
			for i, ctx := range event.Interrupt.Contexts {
				// Copy the context wrapper; Info retains its opaque value and references.
				cloned.Interrupt.Contexts[i] = cloneSessionValue(ctx)
			}
		}
	}
	// Copy the wrapper; Data retains its opaque value and references.
	cloned.Extension = cloneSessionValue(event.Extension)
	return &cloned
}

// cloneSessionValue copies a framework-owned struct, not its reference fields.
// The caller copies owned children explicitly and leaves opaque any data shared.
func cloneSessionValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
