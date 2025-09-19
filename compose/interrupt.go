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

func NewInterruptAndRerunErrWithState(info any, state any) error {
	return &interruptAndRerun{Extra: info, State: state}
}

type interruptAndRerun struct {
	Extra any // for end-user, probably for human too
	State any // for persistence, when resuming, use GetRunCtx to fetch it at the interrupt location
}

type CompositeInterruptState interface {
	GetSubStateForPath(p Path) (any, bool)
	IsPathInterrupted(p Path) bool
}

type CompositeInterruptInfo interface {
	GetSubInterruptContexts() []*InterruptCtx
}

func (i *interruptAndRerun) Error() string {
	return fmt.Sprintf("interrupt and rerun: %v", i.Extra)
}

func IsInterruptRerunError(err error) (any, bool) {
	info, _, ok := isInterruptRerunError(err)
	return info, ok
}

func isInterruptRerunError(err error) (info any, state any, ok bool) {
	if errors.Is(err, InterruptAndRerun) {
		return nil, nil, true
	}
	ire := &interruptAndRerun{}
	if errors.As(err, &ire) {
		return ire.Extra, ire.State, true
	}
	return nil, nil, false
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

func (ii *InterruptInfo) GetInterruptContexts() []*InterruptCtx {
	var ctxs []*InterruptCtx
	for subKey, subInfo := range ii.SubGraphs {
		subInterruptCtxs := subInfo.GetInterruptContexts()
		for i := range subInterruptCtxs {
			c := subInterruptCtxs[i]
			c.Paths = append([]Path{Path{Type: PathTypeNode, ID: subKey}}, c.Paths...)
			c.ID = Paths(c.Paths).String()
			ctxs = append(ctxs, c)
		}
	}

	for _, n := range ii.RerunNodes {
		if _, ok := ii.SubGraphs[n]; ok {
			continue
		}

		extra, ok := ii.RerunNodesExtra[n]
		if !ok {
			ctxs = append(ctxs, &InterruptCtx{
				Paths: []Path{},
			})
			continue
		}

		composite, ok := extra.(CompositeInterruptInfo)
		if ok {
			subContexts := composite.GetSubInterruptContexts()
			for i := range subContexts {
				c := subContexts[i]
				c.Paths = append([]Path{Path{Type: PathTypeNode, ID: n}}, c.Paths...)
				c.ID = Paths(c.Paths).String()
				ctxs = append(ctxs, c)
			}
			continue
		}

		paths := []Path{
			{
				Type: PathTypeNode,
				ID:   n,
			},
		}
		ctxs = append(ctxs, &InterruptCtx{
			ID:    Paths(paths).String(),
			Paths: paths,
			Info:  extra,
		})
	}
	return ctxs
}

type PathType string

const (
	PathTypeNode PathType = "node"
	PathTypeTool PathType = "tool"
)

type Path struct {
	Type PathType
	ID   string
}

type InterruptCtx struct {
	ID    string
	Paths []Path
	Info  any
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
