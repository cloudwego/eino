/*
 * Copyright 2024 CloudWeGo Authors
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

package compose

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrExceedMaxSteps graph will throw this error when the number of steps exceeds the maximum number of steps.
var ErrExceedMaxSteps = errors.New("exceeds max steps")

func newUnexpectedInputTypeErr(expected reflect.Type, got reflect.Type) error {
	return fmt.Errorf("unexpected input type. expected: %v, got: %v", expected, got)
}

type defaultImplAction string

const (
	actionInvokeByStream     defaultImplAction = "InvokeByStream"
	actionInvokeByCollect    defaultImplAction = "InvokeByCollect"
	actionInvokeByTransform  defaultImplAction = "InvokeByTransform"
	actionStreamByInvoke     defaultImplAction = "StreamByInvoke"
	actionStreamByTransform  defaultImplAction = "StreamByTransform"
	actionStreamByCollect    defaultImplAction = "StreamByCollect"
	actionCollectByTransform defaultImplAction = "CollectByTransform"
	actionCollectByInvoke    defaultImplAction = "CollectByInvoke"
	actionCollectByStream    defaultImplAction = "CollectByStream"
	actionTransformByStream  defaultImplAction = "TransformByStream"
	actionTransformByCollect defaultImplAction = "TransformByCollect"
	actionTransformByInvoke  defaultImplAction = "TransformByInvoke"
)

func newStreamReadError(err error) error {
	return fmt.Errorf("failed to read from stream. error: %w", err)
}

func newGraphRunError(err error) error {
	return &internalError{
		typ:       internalErrorTypeGraphRun,
		nodePath:  NodePath{},
		origError: err,
	}
}

func wrapGraphNodeError(nodeKey string, err error) error {
	if ok := isInterruptError(err); ok {
		return err
	}
	var ie *internalError
	ok := errors.As(err, &ie)
	if !ok {
		return &internalError{
			typ:       internalErrorTypeNodeRun,
			nodePath:  NodePath{path: []string{nodeKey}},
			origError: err,
		}
	}
	ie.nodePath.path = append([]string{nodeKey}, ie.nodePath.path...)
	return ie
}

type internalErrorType string

const (
	internalErrorTypeNodeRun  = "NodeRunError"
	internalErrorTypeGraphRun = "GraphRunError"
)

type internalError struct {
	typ       internalErrorType
	nodePath  NodePath
	origError error
}

func (i *internalError) Error() string {
	sb := strings.Builder{}
	sb.WriteString(string("[" + i.typ + "] "))
	sb.WriteString(i.origError.Error())
	if len(i.nodePath.path) > 0 {
		sb.WriteString("\n------------------------\n")
		sb.WriteString("node path: [")
		for j := 0; j < len(i.nodePath.path)-1; j++ {
			sb.WriteString(i.nodePath.path[j] + ", ")
		}
		sb.WriteString(i.nodePath.path[len(i.nodePath.path)-1])
		sb.WriteString("]")
	}
	sb.WriteString("")
	return sb.String()
}

func (i *internalError) Unwrap() error {
	return i.origError
}
