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
	"context"
	"fmt"

	"github.com/cloudwego/eino/internal/serialization"
)

func RegisterType(name string, val any) error {
	return serialization.Register(name, val)
}

func GenericRegisterType[T any](name string) error {
	return serialization.GenericRegister[T](name)
}

type CheckPointStore interface {
	Get(ctx context.Context, checkPointID string) ([]byte, bool, error)
	Set(ctx context.Context, checkPointID string, checkPoint []byte) error
}

func WithCheckPointStore(store CheckPointStore) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.checkPointStore = store
	}
}

func WithCheckPointID(checkPointID string) Option {
	return Option{
		checkPointID: &checkPointID,
	}
}

type StateModifier func(ctx context.Context, path []string, state any) error

func WithStateModifier(sm StateModifier) Option {
	return Option{
		stateModifier: sm,
	}
}

type checkpoint struct {
	Channels       map[string]channel
	Inputs         map[string] /*node key*/ any /*input*/
	State          any
	SkipPreHandler bool

	SubGraphs map[string]*checkpoint
}

type nodePathKey struct{}
type stateModifierKey struct{}
type checkPointKey struct{} // *checkpoint

func getNodeKey(ctx context.Context) (*NodePath, bool) {
	if key, ok := ctx.Value(nodePathKey{}).(*NodePath); ok {
		return key, true
	}
	return nil, false
}

func setNodeKey(ctx context.Context, key string) context.Context {
	path, existed := getNodeKey(ctx)
	if !existed || len(path.path) == 0 {
		return context.WithValue(ctx, nodePathKey{}, NewNodePath(key))
	}
	return context.WithValue(ctx, nodePathKey{}, NewNodePath(append(path.path, key)...))
}

func getStateModifier(ctx context.Context) StateModifier {
	if sm, ok := ctx.Value(stateModifierKey{}).(StateModifier); ok {
		return sm
	}
	return nil
}

func setStateModifier(ctx context.Context, modifier StateModifier) context.Context {
	return context.WithValue(ctx, stateModifierKey{}, modifier)
}

func getCheckPointFromStore(ctx context.Context, id string, cpr *checkPointer, isStream bool) (cp *checkpoint, err error) {
	cp, existed, err := cpr.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !existed {
		return nil, nil
	}

	return cp, nil
}

func setCheckPointToCtx(ctx context.Context, cp *checkpoint) context.Context {
	return context.WithValue(ctx, checkPointKey{}, cp)
}

func getCheckPointFromCtx(ctx context.Context) *checkpoint {
	if cp, ok := ctx.Value(checkPointKey{}).(*checkpoint); ok {
		return cp
	}
	return nil
}

func forwardCheckPoint(ctx context.Context, nodeKey string) context.Context {
	cp := getCheckPointFromCtx(ctx)
	if cp == nil {
		return ctx
	}
	if subCP, ok := cp.SubGraphs[nodeKey]; ok {
		return context.WithValue(ctx, checkPointKey{}, subCP)
	}
	return context.WithValue(ctx, checkPointKey{}, (*checkpoint)(nil))
}

func newCheckPointer(
	inputPairs, outputPairs map[string]streamConvertPair,
	store CheckPointStore,
) *checkPointer {
	return &checkPointer{
		sc:    newStreamConverter(inputPairs, outputPairs),
		store: store,
	}
}

type checkPointer struct {
	sc    *streamConverter
	store CheckPointStore
}

func (c *checkPointer) get(ctx context.Context, id string) (*checkpoint, bool, error) {
	data, existed, err := c.store.Get(ctx, id)
	if err != nil || existed == false {
		return nil, existed, err
	}

	cp := &checkpoint{}
	value, err := serialization.Unmarshal(data)
	if err != nil {
		return nil, false, err
	}
	cp = value.(*checkpoint)

	return cp, true, nil
}

func (c *checkPointer) set(ctx context.Context, id string, cp *checkpoint) error {
	data, err := serialization.Marshal(cp)
	if err != nil {
		return err
	}

	return c.store.Set(ctx, id, data)
}

func (c *checkPointer) convertCheckPoint(cp *checkpoint, isStream bool) (err error) {
	for _, ch := range cp.Channels {
		err = ch.convertValues(func(m map[string]any) error {
			return c.sc.convertOutputs(isStream, m)
		})
		if err != nil {
			return err
		}
	}

	err = c.sc.convertInputs(isStream, cp.Inputs)
	if err != nil {
		return err
	}

	return nil
}

func (c *checkPointer) restoreCheckPoint(cp *checkpoint, isStream bool) (err error) {
	for _, ch := range cp.Channels {
		err = ch.convertValues(func(m map[string]any) error {
			return c.sc.restoreOutputs(isStream, m)
		})
		if err != nil {
			return err
		}
	}

	err = c.sc.restoreInputs(isStream, cp.Inputs)
	if err != nil {
		return err
	}

	return nil
}

func newStreamConverter(inputPairs, outputPairs map[string]streamConvertPair) *streamConverter {
	return &streamConverter{
		inputPairs:  inputPairs,
		outputPairs: outputPairs,
	}
}

type streamConverter struct {
	inputPairs, outputPairs map[string]streamConvertPair
}

func (s *streamConverter) convertInputs(isStream bool, values map[string]any) error {
	return convert(values, s.inputPairs, isStream)
}

func (s *streamConverter) restoreInputs(isStream bool, values map[string]any) error {
	return restore(values, s.inputPairs, isStream)
}

func (s *streamConverter) convertOutputs(isStream bool, values map[string]any) error {
	return convert(values, s.outputPairs, isStream)
}

func (s *streamConverter) restoreOutputs(isStream bool, values map[string]any) error {
	return restore(values, s.outputPairs, isStream)
}

func convert(values map[string]any, convPairs map[string]streamConvertPair, isStream bool) error {
	if !isStream {
		return nil
	}
	for key, v := range values {
		convPair, ok := convPairs[key]
		if !ok {
			return fmt.Errorf("checkpoint conv stream fail, node[%s] have not been registered", key)
		}
		sr, ok := v.(streamReader)
		if !ok {
			return fmt.Errorf("checkpoint conv stream fail, value of [%s] isn't stream", key)
		}
		nValue, err := convPair.concatStream(sr)
		if err != nil {
			return err
		}
		values[key] = nValue
	}
	return nil
}

func restore(values map[string]any, convPairs map[string]streamConvertPair, isStream bool) error {
	if !isStream {
		return nil
	}
	for key, v := range values {
		convPair, ok := convPairs[key]
		if !ok {
			return fmt.Errorf("checkpoint restore stream fail, node[%s] have not been registered", key)
		}
		sr, err := convPair.restoreStream(v)
		if err != nil {
			return err
		}
		values[key] = sr
	}
	return nil
}
