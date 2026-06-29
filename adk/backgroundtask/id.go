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

package backgroundtask

import (
	"math/rand"
	"time"
)

// Task-id layout: a positive int64 (63 usable bits) packed as
//
//	[ 41 bits ms timestamp ][ 12 bits sequence ][ 10 bits random ]
//
// Uniqueness within a process is guaranteed by (timestamp, sequence): the
// sequence resets each millisecond and increments for every id minted within the
// same millisecond, all under the Manager lock. If more than 2^12 ids are minted
// in a single millisecond the generator spins to the next millisecond rather than
// wrapping the sequence, so (timestamp, sequence) never repeats. The random low
// bits only make ids look unordered/unpredictable; they are not relied upon for
// uniqueness.
//
// 41 bits of milliseconds covers ~69 years; 12 bits allows 4096 ids per
// millisecond before the generator advances to the next millisecond.
const (
	idSeqBits    = 12
	idRandomBits = 10
	idSeqLimit   = 1 << idSeqBits
	idRandomMask = (1 << idRandomBits) - 1
)

// nextRawID packs the next task id integer. Must be called with m.mu held, as it
// reads and advances m.seq / m.lastMs.
func (m *Manager) nextRawID() int64 {
	ms := time.Now().UnixMilli()
	switch {
	case ms > m.lastMs:
		m.lastMs = ms
		m.seq = 0
	default:
		// Same millisecond (or a backward clock step): keep the id monotonic by
		// staying on lastMs and advancing the sequence. On sequence overflow, move
		// to the next millisecond so (timestamp, sequence) stays unique.
		ms = m.lastMs
		m.seq++
		if m.seq >= idSeqLimit {
			ms = m.waitNextMs(m.lastMs)
			m.lastMs = ms
			m.seq = 0
		}
	}

	//nolint:gosec // non-cryptographic: random bits only diffuse the id's look.
	r := int64(rand.Intn(idRandomMask + 1))
	return (ms << (idSeqBits + idRandomBits)) | (m.seq << idRandomBits) | r
}

// waitNextMs busy-waits until the wall clock advances past prevMs. Reached only
// when more than 2^12 ids are minted within one millisecond.
func (m *Manager) waitNextMs(prevMs int64) int64 {
	ms := time.Now().UnixMilli()
	for ms <= prevMs {
		ms = time.Now().UnixMilli()
	}
	return ms
}

// base62 encodes a non-negative int64 using [0-9A-Za-z]. It is the compact,
// URL-safe textual form of a task id's integer.
func base62(n int64) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0"
	}
	var buf [11]byte // ceil(63 / log2(62)) = 11
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = alphabet[n%62]
		n /= 62
	}
	return string(buf[i:])
}

// defaultTaskIDPrefix is used when a task has no Type tag.
const defaultTaskIDPrefix = "task"

// taskIDPrefix returns the id prefix for a task type, falling back to a generic
// prefix when the type is empty. The type tag (e.g. "bash", "subagent") makes ids
// self-describing: "bash_3Fa9...".
func taskIDPrefix(taskType string) string {
	if taskType == "" {
		return defaultTaskIDPrefix
	}
	return taskType
}
