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
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*ForegroundTimeoutError](
		"_eino_adk_backgroundtask_foreground_timeout_error",
	)
}

// ForegroundTimeoutError reports that the foreground observation budget for a
// task expired. It unwraps to context.DeadlineExceeded for compatibility.
type ForegroundTimeoutError struct {
	// Timeout is the configured foreground observation budget.
	Timeout time.Duration
	// TaskID is the pre-allocated task ID. The task may not have been persisted.
	TaskID string
}

func (e *ForegroundTimeoutError) Error() string {
	return fmt.Sprintf(
		"backgroundtask: foreground task %q timed out after %s",
		e.TaskID,
		e.Timeout,
	)
}

// Unwrap makes errors.Is(err, context.DeadlineExceeded) report true.
func (e *ForegroundTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}
