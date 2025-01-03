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

package internal

import (
	"errors"
	"fmt"
	"reflect"
)

// ErrExceedMaxSteps graph will throw this error when the number of steps exceeds the maximum number of steps.
var ErrExceedMaxSteps = errors.New("exceeds max steps")

func newUnexpectedInputTypeErr(expected reflect.Type, got reflect.Type) error {
	return fmt.Errorf("unexpected input type. expected: %v, got: %v", expected, got)
}

type defaultImplErrCausedType string
type defaultImplAction string

const (
	streamConcat defaultImplErrCausedType = "concat stream items"
	internalCall defaultImplErrCausedType = "call internal action"
)
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

func newDefaultImplErr(action defaultImplAction, causedType defaultImplErrCausedType, causedErr error) error {
	return fmt.Errorf(
		"default implementation: '%s' got error, when try to %s, err: \n%w", action, causedType, causedErr)
}

func newStreamReadError(err error) error {
	return fmt.Errorf("failed to read from stream. error: %w", err)
}
