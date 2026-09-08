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

package background

import (
	"crypto/rand"
	"encoding/base64"
)

const taskIDEntropyBytes = 16
const maxTaskIDKindBytes = 64

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

func defaultTaskID(kind string) (string, error) {
	var entropy [taskIDEntropyBytes]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return taskIDPrefix(kind) + "_" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

func validTaskIDKind(kind string) bool {
	if len(kind) > maxTaskIDKindBytes {
		return false
	}
	for i := 0; i < len(kind); i++ {
		c := kind[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
