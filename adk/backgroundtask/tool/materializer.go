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

package tool

import "context"

// OutputMaterializer optionally projects authoritative keyed output records to
// a deterministic file or object. AppendOutput must be idempotent by
// (TaskID, SourceID) and apply distinct records in Sequence order.
type OutputMaterializer interface {
	ReserveOutput(context.Context, *ReserveOutputRequest) (string, error)
	AppendOutput(context.Context, *MaterializeOutputRequest) error
}

// ReserveOutputRequest identifies the task whose derived output path is reserved.
type ReserveOutputRequest struct {
	TaskID string
}

// MaterializeOutputRequest describes one authoritative keyed output record.
type MaterializeOutputRequest struct {
	TaskID   string
	SourceID string
	Sequence int64
	Path     string
	Data     []byte
}
