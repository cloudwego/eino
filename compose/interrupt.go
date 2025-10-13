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

	"github.com/cloudwego/eino/schema"
)

func WithInterruptBeforeNodes(nodes []string) GraphCompileOption {
	return func(options *graphCompileOptions) {
		options.interruptBeforeNodes = nodes
	}
}

func WithInterruptAfterNodes(nodes []string) GraphCompileOption {
	return func(options *graphCompileOptions) {
		options.interruptAfterNodes = nodes
	}
}

var InterruptAndRerun = errors.New("interrupt and rerun")

func NewInterruptAndRerunErr(extra any) error {
	return &interruptAndRerun{Extra: extra}
}

type interruptAndRerun struct {
	Extra any
}

func (i *interruptAndRerun) Error() string {
	return fmt.Sprintf("interrupt and rerun: %v", i.Extra)
}

func IsInterruptRerunError(err error) (any, bool) {
	if errors.Is(err, InterruptAndRerun) {
		return nil, true
	}
	ire := &interruptAndRerun{}
	if errors.As(err, &ire) {
		return ire.Extra, true
	}
	return nil, false
}

type InterruptInfo struct {
	State           any
	BeforeNodes     []string
	AfterNodes      []string
	RerunNodes      []string
	RerunNodesExtra map[string]any
	SubGraphs       map[string]*InterruptInfo
}

func init() {
	schema.RegisterName[*InterruptInfo]("_eino_compose_interrupt_info") // TODO: check if this is really needed when refactoring adk resume
}

func ExtractInterruptInfo(err error) (info *InterruptInfo, existed bool) {
	if err == nil {
		return nil, false
	}
	var iE *interruptError
	if errors.As(err, &iE) {
		return iE.Info, true
	}
	var sIE *subGraphInterruptError
	if errors.As(err, &sIE) {
		return sIE.Info, true
	}
	return nil, false
}

type interruptError struct {
	Info *InterruptInfo
}

func (e *interruptError) Error() string {
	return fmt.Sprintf("interrupt happened, info: %+v", e.Info)
}

func isSubGraphInterrupt(err error) *subGraphInterruptError {
	if err == nil {
		return nil
	}
	var iE *subGraphInterruptError
	if errors.As(err, &iE) {
		return iE
	}
	return nil
}

type subGraphInterruptError struct {
	Info       *InterruptInfo
	CheckPoint *checkpoint
}

func (e *subGraphInterruptError) Error() string {
	return fmt.Sprintf("interrupt happened, info: %+v", e.Info)
}

func isInterruptError(err error) bool {
	if _, ok := ExtractInterruptInfo(err); ok {
		return true
	}
	if info := isSubGraphInterrupt(err); info != nil {
		return true
	}
	if _, ok := IsInterruptRerunError(err); ok {
		return true
	}

	return false
}
