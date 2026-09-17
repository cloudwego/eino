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

package sseha

import "errors"

var (
	// ErrSessionNotFound is returned when the requested session does not exist
	// in the metadata store.
	ErrSessionNotFound = errors.New("sseha: session not found")

	// ErrSessionAlreadyExists is returned when attempting to register a session
	// with an ID that already exists.
	ErrSessionAlreadyExists = errors.New("sseha: session already exists")

	// ErrVersionConflict is returned when an optimistic concurrency update fails
	// because the stored version does not match the expected version.
	ErrVersionConflict = errors.New("sseha: version conflict")

	// ErrSessionLocked is returned when a session migration cannot proceed
	// because another node holds the migration lock.
	ErrSessionLocked = errors.New("sseha: session is locked by another node")

	// ErrNodeNotFound is returned when the requested node is not registered.
	ErrNodeNotFound = errors.New("sseha: node not found")

	// ErrNodeNotAlive is returned when the target node for forwarding/migration
	// is detected as dead.
	ErrNodeNotAlive = errors.New("sseha: node is not alive")

	// ErrBarrierNotReleased is returned when attempting to process a session
	// before the migration barrier has been released.
	ErrBarrierNotReleased = errors.New("sseha: migration barrier not released")

	// ErrEventGap is returned when event replay cannot bridge the gap between
	// the client's Last-Event-ID and the oldest buffered event.
	ErrEventGap = errors.New("sseha: event gap too large for replay")

	// ErrSessionClosed is returned when attempting to operate on a closed session.
	ErrSessionClosed = errors.New("sseha: session is closed")

	// ErrSubscriptionExists is returned when attempting to subscribe to a session
	// that already has an active subscription on this node.
	ErrSubscriptionExists = errors.New("sseha: subscription already exists")

	// ErrManagerClosed is returned when operations are attempted on a closed
	// session manager.
	ErrManagerClosed = errors.New("sseha: manager is closed")
)
