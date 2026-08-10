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

// OutputMaterializer optionally projects caller-identified task events to a
// deterministic file or object. AppendOutput must durably deduplicate by
// (TaskID, EventID) for the task's recovery and retention lifetime. Distinct
// events must be applied in call order; EventID is opaque and must not be
// sorted. Recoverable update sources must therefore replay in stable order.
type OutputMaterializer interface {
	// ReserveOutput must return the same path when repeated with one TaskID.
	// A reservation may remain unused when later task submission fails.
	ReserveOutput(context.Context, *ReserveOutputRequest) (string, error)
	AppendOutput(context.Context, *MaterializeOutputRequest) error
}

// ReserveOutputRequest identifies the task whose derived output path is reserved.
type ReserveOutputRequest struct {
	TaskID string
}

// MaterializeOutputRequest describes one caller-identified progress event.
type MaterializeOutputRequest struct {
	TaskID  string
	EventID string
	Path    string
	Data    []byte
}
