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
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/serialization"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*checkpoint]("_eino_checkpoint")
	schema.RegisterName[*checkpointLayoutSentinelV1]("_eino_checkpoint_layout_v1")
	schema.RegisterName[*dagChannel]("_eino_dag_channel")
	schema.RegisterName[*pregelChannel]("_eino_pregel_channel")
	schema.RegisterName[dependencyState]("_eino_dependency_state")
	_ = serialization.GenericRegister[channel]("_eino_channel")
}

// RegisterSerializableType registers a custom type for eino serialization.
// This allows eino to properly serialize and deserialize custom types.
// Both custom interfaces and structs need to be registered using this function.
// Types only need to be registered once - pointers and other references will be handled automatically.
// All built-in eino types are already registered.
// Parameters:
// - name: A unique identifier for the type being registered (should not start with "_eino")
// - T: The generic type parameter representing the type to register
// Returns:
// - error: An error if registration fails (e.g., if the type is already registered)
// Deprecated: RegisterSerializableType is deprecated. Use schema.RegisterName[T](name) instead.
func RegisterSerializableType[T any](name string) (err error) {
	return serialization.GenericRegister[T](name)
}

type CheckPointStore = core.CheckPointStore

type Serializer interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

// WithCheckPointStore sets the checkpoint store implementation for a graph.
func WithCheckPointStore(store CheckPointStore) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.checkPointStore = store
	}
}

// WithSerializer sets the serializer used to persist checkpoint state.
func WithSerializer(serializer Serializer) GraphCompileOption {
	return func(o *graphCompileOptions) {
		o.serializer = serializer
	}
}

// WithCheckPointID sets the checkpoint ID to load from and write to by default.
func WithCheckPointID(checkPointID string) Option {
	return Option{
		checkPointID: &checkPointID,
	}
}

// WithWriteToCheckPointID specifies a different checkpoint ID to write to.
// If not provided, the checkpoint ID from WithCheckPointID will be used for writing.
// This is useful for scenarios where you want to load from an existed checkpoint
// but save the progress to a new, separate checkpoint.
func WithWriteToCheckPointID(checkPointID string) Option {
	return Option{
		writeToCheckPointID: &checkPointID,
	}
}

// WithForceNewRun forces the graph to run from the beginning, ignoring any checkpoints.
func WithForceNewRun() Option {
	return Option{
		forceNewRun: true,
	}
}

// StateModifier modifies state during checkpoint operations for a given node path.
type StateModifier func(ctx context.Context, path NodePath, state any) error

// WithStateModifier installs a state modifier invoked during checkpoint read/write.
func WithStateModifier(sm StateModifier) Option {
	return Option{
		stateModifier: sm,
	}
}

const (
	checkpointStateLayoutVersionV1 = 1
	checkpointLayoutSentinelID     = "_eino_checkpoint_layout"
)

type checkpointLayoutSentinelV1 struct {
	Version int
}

type checkpoint struct {
	StateLayoutVersion int

	Channels       map[string]channel
	Inputs         map[string] /*node key*/ any /*input*/
	State          any
	SkipPreHandler map[string]bool
	RerunNodes     []string

	SubGraphs map[string]*checkpoint

	InterruptID2Addr  map[string]Address
	InterruptID2State map[string]core.InterruptState

	layoutMetadataValidated bool
}

type stateModifierKey struct{}
type checkPointKey struct{} // *checkpoint

func withSubGraphCheckpointPublisher(opts []any) (
	[]any,
	<-chan *subGraphInterruptError,
) {
	ready := make(chan *subGraphInterruptError, 1)
	childOpts := append([]any(nil), opts...)
	childOpts = append(childOpts, Option{
		subGraphCheckpointPublisher: func(checkpoint *subGraphInterruptError) {
			ready <- checkpoint
		},
	})
	return childOpts, ready
}

func getSubGraphCheckpointPublisher(opts ...Option) func(*subGraphInterruptError) {
	for _, opt := range opts {
		if opt.subGraphCheckpointPublisher != nil {
			return opt.subGraphCheckpointPublisher
		}
	}
	return nil
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

func getCheckPointFromStore(ctx context.Context, id string, cpr *checkPointer) (cp *checkpoint, err error) {
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
	ctx = core.PopulateInterruptState(ctx, cp.InterruptID2Addr, cp.InterruptID2State)
	return context.WithValue(ctx, checkPointKey{}, cp)
}

func getCheckPointFromCtx(ctx context.Context) *checkpoint {
	if cp, ok := ctx.Value(checkPointKey{}).(*checkpoint); ok {
		return cp
	}
	return nil
}

func forwardCheckPoint(ctx context.Context, nodeKey string) (context.Context, error) {
	cp := getCheckPointFromCtx(ctx)
	if cp == nil {
		return ctx, nil
	}

	if subCP, ok := cp.SubGraphs[nodeKey]; ok {
		var err error
		if subCP.StateLayoutVersion == checkpointStateLayoutVersionV1 {
			if err = consumeCheckpointLayoutMetadata(subCP); err != nil {
				return nil, err
			}
			ctx, err = core.MergeInterruptState(ctx, subCP.InterruptID2Addr, subCP.InterruptID2State)
			if err != nil {
				return nil, fmt.Errorf("failed to merge subgraph interrupt state: %w", err)
			}
		}
		delete(cp.SubGraphs, nodeKey) // only forward once after successful validation and merge
		return context.WithValue(ctx, checkPointKey{}, subCP), nil
	}
	return context.WithValue(ctx, checkPointKey{}, (*checkpoint)(nil)), nil
}

func validateCheckpointLayoutMetadata(cp *checkpoint) error {
	if cp == nil || cp.layoutMetadataValidated {
		return nil
	}
	if cp.StateLayoutVersion == 0 {
		if _, exists := cp.InterruptID2State[checkpointLayoutSentinelID]; exists {
			return errors.New("legacy checkpoint contains a versioned state layout sentinel")
		}
		return nil
	}
	if cp.StateLayoutVersion != checkpointStateLayoutVersionV1 {
		return fmt.Errorf("checkpoint requires a newer Eino version: unsupported state layout version %d",
			cp.StateLayoutVersion)
	}
	state, ok := cp.InterruptID2State[checkpointLayoutSentinelID]
	if !ok {
		return errors.New("checkpoint state layout sentinel is missing")
	}
	sentinel, ok := state.State.(*checkpointLayoutSentinelV1)
	if !ok || sentinel == nil || sentinel.Version != checkpointStateLayoutVersionV1 {
		return fmt.Errorf("checkpoint has invalid state layout sentinel %T", state.State)
	}
	return nil
}

func consumeCheckpointLayoutMetadata(cp *checkpoint) error {
	if err := validateCheckpointLayoutMetadata(cp); err != nil {
		return err
	}
	if cp == nil || cp.StateLayoutVersion == 0 || cp.layoutMetadataValidated {
		return nil
	}
	delete(cp.InterruptID2State, checkpointLayoutSentinelID)
	cp.layoutMetadataValidated = true
	return nil
}

func initializeCheckpointLayoutV1(cp *checkpoint) error {
	cp.StateLayoutVersion = checkpointStateLayoutVersionV1
	if cp.InterruptID2State == nil {
		cp.InterruptID2State = make(map[string]core.InterruptState)
	}
	for _, id := range sortedCheckpointMapKeys(cp.InterruptID2State) {
		if isCheckpointMetadataID(id) {
			return fmt.Errorf("interrupt ID %q uses reserved checkpoint metadata prefix", id)
		}
	}
	cp.InterruptID2State[checkpointLayoutSentinelID] = core.InterruptState{
		State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
	}
	return nil
}

func isCheckpointMetadataID(id string) bool {
	return strings.HasPrefix(id, "_eino_")
}

func newCheckPointer(
	inputPairs, outputPairs map[string]streamConvertPair,
	store CheckPointStore,
	serializer Serializer,
) *checkPointer {
	if serializer == nil {
		serializer = &serialization.InternalSerializer{}
	}
	return &checkPointer{
		sc:         newStreamConverter(inputPairs, outputPairs),
		store:      store,
		serializer: serializer,
	}
}

type checkPointer struct {
	sc         *streamConverter
	store      CheckPointStore
	serializer Serializer
}

func (c *checkPointer) get(ctx context.Context, id string) (*checkpoint, bool, error) {
	data, existed, err := c.store.Get(ctx, id)
	if err != nil || existed == false {
		return nil, existed, err
	}

	cp := &checkpoint{}
	err = c.serializer.Unmarshal(data, cp)
	if err != nil {
		return nil, false, err
	}

	return cp, true, nil
}

func (c *checkPointer) set(ctx context.Context, id string, cp *checkpoint) error {
	normalizeCheckpointTypedNilInputs(cp)

	data, err := c.serializer.Marshal(cp)
	if err != nil {
		return err
	}

	return c.store.Set(ctx, id, data)
}

func normalizeCheckpointTypedNilInputs(cp *checkpoint) {
	if cp == nil {
		return
	}
	for key, input := range cp.Inputs {
		if isTypedNil(input) {
			cp.Inputs[key] = nil
		}
	}
	for _, sub := range cp.SubGraphs {
		normalizeCheckpointTypedNilInputs(sub)
	}
}

func isTypedNil(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// MigrateCheckpointState is an advanced compatibility utility for checkpoint upgrades.
//
// It decodes checkpoint bytes using the given serializer, applies migrate to checkpoint.State and
// all nested SubGraphs' states, then re-encodes the checkpoint.
//
// Typical use cases:
//   - Resume-time migration when you changed your graph state type/schema and need to load old
//     checkpoints without discarding them.
//   - Framework-level backward compatibility (e.g. ADK upgrading checkpoints across versions).
//
// Migrate callback contract:
//   - Returns (newState, changed, error).
//   - If changed is false, the state is left as-is.
//   - If error is non-nil, migration stops and the error is returned to the caller.
//
// The original bytes are returned only if no state was changed anywhere in the checkpoint tree.
// Checkpoint metadata and interrupt-state sentinels are preserved and are not passed to migrate.
// Compact ToolsNode references are hydrated before migrate runs. If state changes, they are
// rebound to the new state when possible and otherwise written inline.
// A checkpoint written by a newer Eino version may fail decoding before migration can run.
func MigrateCheckpointState(data []byte, serializer Serializer, migrate func(state any) (any, bool, error)) ([]byte, error) {
	cp := &checkpoint{}
	if err := serializer.Unmarshal(data, cp); err != nil {
		return nil, fmt.Errorf("failed to decode checkpoint for migration; checkpoint may require a newer Eino version: %w", err)
	}
	if err := hydrateCheckpointToolsNodeState(cp); err != nil {
		return nil, fmt.Errorf("failed to hydrate checkpoint tool state for migration: %w", err)
	}
	changed, err := migrateCheckpoint(cp, migrate)
	if err != nil {
		return nil, err
	}
	if !changed {
		return data, nil
	}
	compactCheckpointToolsNodeState(cp)
	return serializer.Marshal(cp)
}

// CheckpointValueKind identifies a value-bearing part of a checkpoint.
// Future releases may add kinds. Visitors should ignore unknown kinds, and
// transformers should return changed=false for them.
type CheckpointValueKind string

const (
	// CheckpointValueState identifies a graph's local state.
	CheckpointValueState CheckpointValueKind = "state"
	// CheckpointValueInput identifies a persisted node input.
	CheckpointValueInput CheckpointValueKind = "input"
	// CheckpointValueChannel identifies a value buffered in a graph channel.
	CheckpointValueChannel CheckpointValueKind = "channel"
	// CheckpointValueInterruptState identifies a component's persisted interrupt state.
	CheckpointValueInterruptState CheckpointValueKind = "interrupt_state"
	// CheckpointValueInterruptLayerPayload identifies layer-specific interrupt metadata.
	CheckpointValueInterruptLayerPayload CheckpointValueKind = "interrupt_layer_payload"
)

// CheckpointValueLocation identifies a value within one graph checkpoint.
type CheckpointValueLocation struct {
	// Kind identifies the checkpoint field containing the value.
	Kind CheckpointValueKind
	// Key is the node key for inputs, the channel key for channel values, or
	// the interrupt ID for interrupt state and layer payload values. It is empty
	// for graph state.
	Key string
	// ValueKey is set only for channel values and identifies the channel
	// predecessor. It is empty for every other kind.
	ValueKey string
}

// WalkCheckpointValues visits value-bearing fields in a serialized checkpoint.
// Within each graph it visits state, interrupt states and layer payloads,
// inputs, and channel values before recursively visiting subgraphs. Map keys
// and subgraph keys are visited in lexical order, and internal metadata entries
// are skipped. A nil serializer uses Eino's internal serializer. The callback
// must treat value and path.GetPath() as read-only. Compact ToolsNode references
// are hydrated, so the callback observes logical values rather than their wire
// representation.
func WalkCheckpointValues(data []byte, serializer Serializer,
	visit func(path NodePath, location CheckpointValueLocation, value any) error,
) error {
	if visit == nil {
		return errors.New("checkpoint value visitor is nil")
	}
	if serializer == nil {
		serializer = &serialization.InternalSerializer{}
	}
	cp := &checkpoint{}
	if err := serializer.Unmarshal(data, cp); err != nil {
		return fmt.Errorf("failed to decode checkpoint for inspection: %w", err)
	}
	if err := hydrateCheckpointToolsNodeState(cp); err != nil {
		return fmt.Errorf("failed to hydrate checkpoint tool state for inspection: %w", err)
	}
	return walkCheckpointValues(cp, nil, func(path NodePath, location CheckpointValueLocation,
		value any) (any, bool, error) {
		return value, false, visit(path, location, value)
	})
}

// TransformCheckpointValues replaces selected value-bearing fields in a
// serialized checkpoint. Unknown values can be left unchanged by returning
// changed=false. The callback must treat the input value and path.GetPath() as
// read-only and return a replacement for changes. Checkpoint metadata and
// interrupt routing are preserved and internal metadata entries are not passed
// to the callback. A nil serializer uses Eino's internal serializer. If no
// value is replaced, the original byte slice is returned unchanged. Compact
// ToolsNode references are hydrated before the callback and rebound or stored
// inline after changes.
func TransformCheckpointValues(data []byte, serializer Serializer,
	transform func(path NodePath, location CheckpointValueLocation, value any) (
		replacement any, changed bool, err error),
) ([]byte, error) {
	if transform == nil {
		return nil, errors.New("checkpoint value transformer is nil")
	}
	if serializer == nil {
		serializer = &serialization.InternalSerializer{}
	}
	cp := &checkpoint{}
	if err := serializer.Unmarshal(data, cp); err != nil {
		return nil, fmt.Errorf("failed to decode checkpoint for transformation: %w", err)
	}
	if err := hydrateCheckpointToolsNodeState(cp); err != nil {
		return nil, fmt.Errorf("failed to hydrate checkpoint tool state for transformation: %w", err)
	}
	changed, err := transformCheckpointValues(cp, nil, transform)
	if err != nil {
		return nil, err
	}
	if !changed {
		return data, nil
	}
	compactCheckpointToolsNodeState(cp)
	transformed, err := serializer.Marshal(cp)
	if err != nil {
		return nil, fmt.Errorf("failed to encode transformed checkpoint: %w", err)
	}
	return transformed, nil
}

type checkpointValueTransform func(path NodePath, location CheckpointValueLocation,
	value any) (replacement any, changed bool, err error)

func walkCheckpointValues(cp *checkpoint, path []string, transform checkpointValueTransform) error {
	_, err := transformCheckpointValues(cp, path, transform)
	return err
}

func transformCheckpointValues(cp *checkpoint, path []string,
	transform checkpointValueTransform) (bool, error) {
	if cp == nil {
		return false, nil
	}
	changed := false
	nodePath := *NewNodePath(append([]string(nil), path...)...)

	replacement, replaced, err := transform(nodePath,
		CheckpointValueLocation{Kind: CheckpointValueState}, cp.State)
	if err != nil {
		return false, err
	}
	if replaced {
		cp.State = replacement
		changed = true
	}

	interruptIDs := sortedCheckpointMapKeys(cp.InterruptID2State)
	for _, id := range interruptIDs {
		if isCheckpointMetadataID(id) {
			continue
		}
		state := cp.InterruptID2State[id]
		replacement, replaced, err = transform(nodePath,
			CheckpointValueLocation{Kind: CheckpointValueInterruptState, Key: id}, state.State)
		if err != nil {
			return false, err
		}
		if replaced {
			state.State = replacement
			changed = true
		}
		replacement, replaced, err = transform(nodePath,
			CheckpointValueLocation{Kind: CheckpointValueInterruptLayerPayload, Key: id},
			state.LayerSpecificPayload)
		if err != nil {
			return false, err
		}
		if replaced {
			state.LayerSpecificPayload = replacement
			changed = true
		}
		cp.InterruptID2State[id] = state
	}

	inputKeys := sortedCheckpointMapKeys(cp.Inputs)
	for _, key := range inputKeys {
		replacement, replaced, err = transform(nodePath,
			CheckpointValueLocation{Kind: CheckpointValueInput, Key: key}, cp.Inputs[key])
		if err != nil {
			return false, err
		}
		if replaced {
			cp.Inputs[key] = replacement
			changed = true
		}
	}

	channelKeys := make([]string, 0, len(cp.Channels))
	for key := range cp.Channels {
		channelKeys = append(channelKeys, key)
	}
	sort.Strings(channelKeys)
	for _, channelKey := range channelKeys {
		ch := cp.Channels[channelKey]
		if ch == nil {
			continue
		}
		err = ch.convertValues(func(values map[string]any) error {
			valueKeys := sortedCheckpointMapKeys(values)
			for _, valueKey := range valueKeys {
				replacement, replaced, err = transform(nodePath, CheckpointValueLocation{
					Kind:     CheckpointValueChannel,
					Key:      channelKey,
					ValueKey: valueKey,
				}, values[valueKey])
				if err != nil {
					return err
				}
				if replaced {
					values[valueKey] = replacement
					changed = true
				}
			}
			return nil
		})
		if err != nil {
			return false, err
		}
	}

	subGraphKeys := make([]string, 0, len(cp.SubGraphs))
	for key := range cp.SubGraphs {
		subGraphKeys = append(subGraphKeys, key)
	}
	sort.Strings(subGraphKeys)
	for _, key := range subGraphKeys {
		if cp.SubGraphs[key] == nil {
			return false, fmt.Errorf("subgraph checkpoint %q is nil", key)
		}
		childPath := append(append([]string(nil), path...), key)
		childChanged, err := transformCheckpointValues(cp.SubGraphs[key], childPath, transform)
		if err != nil {
			return false, err
		}
		changed = changed || childChanged
	}
	return changed, nil
}

func sortedCheckpointMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// migrateCheckpoint recursively applies migrate to cp.State and all SubGraphs.
func migrateCheckpoint(cp *checkpoint, migrate func(state any) (any, bool, error)) (bool, error) {
	if cp == nil {
		return false, errors.New("checkpoint is nil")
	}
	anyChanged := false
	if cp.State != nil {
		newState, changed, err := migrate(cp.State)
		if err != nil {
			return false, err
		}
		if changed {
			cp.State = newState
			anyChanged = true
		}
	}
	keys := sortedCheckpointMapKeys(cp.SubGraphs)
	for _, key := range keys {
		sub := cp.SubGraphs[key]
		if sub == nil {
			return false, fmt.Errorf("subgraph checkpoint %q is nil", key)
		}
		changed, err := migrateCheckpoint(sub, migrate)
		if err != nil {
			return false, err
		}
		if changed {
			anyChanged = true
		}
	}
	return anyChanged, nil
}

// convertCheckPoint if value in checkpoint is streamReader, convert it to non-stream
func (c *checkPointer) convertCheckPoint(cp *checkpoint, isStream bool) (err error) {
	ignoreInterrupt := func(err error) bool {
		// The checkpoint maps already carry this control signal. A copied data
		// stream may surface it again while being materialized.
		return checkpointContainsInterrupt(cp, err)
	}
	for _, ch := range cp.Channels {
		err = ch.convertValues(func(m map[string]any) error {
			return c.sc.convertOutputs(isStream, m, ignoreInterrupt)
		})
		if err != nil {
			return err
		}
	}

	err = c.sc.convertInputs(isStream, cp.Inputs, ignoreInterrupt)
	if err != nil {
		return err
	}

	return nil
}

func checkpointContainsInterrupt(cp *checkpoint, err error) bool {
	if cp == nil || err == nil {
		return false
	}

	signal := &core.InterruptSignal{}
	if errors.As(err, &signal) {
		_, ok := cp.InterruptID2Addr[signal.ID]
		return ok
	}

	if nested := isSubGraphInterrupt(err); nested != nil && nested.signal != nil {
		_, ok := cp.InterruptID2Addr[nested.signal.ID]
		return ok
	}

	return false
}

// convertCheckPoint convert values in checkpoint to streamReader if needed
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

func (s *streamConverter) convertInputs(isStream bool, values map[string]any, ignoreError func(error) bool) error {
	return convert(values, s.inputPairs, isStream, ignoreError)
}

func (s *streamConverter) restoreInputs(isStream bool, values map[string]any) error {
	return restore(values, s.inputPairs, isStream)
}

func (s *streamConverter) convertOutputs(isStream bool, values map[string]any, ignoreError func(error) bool) error {
	return convert(values, s.outputPairs, isStream, ignoreError)
}

func (s *streamConverter) restoreOutputs(isStream bool, values map[string]any) error {
	return restore(values, s.outputPairs, isStream)
}

func convert(values map[string]any, convPairs map[string]streamConvertPair, isStream bool, ignoreError func(error) bool) error {
	if !isStream {
		return nil
	}
	for key, v := range values {
		convPair, ok := convPairs[key]
		if !ok {
			return fmt.Errorf("checkpoint conv stream fail, node[%s] have not been registered", key)
		}
		if convPair.concatStream == nil {
			return fmt.Errorf("checkpoint conv stream fail, node[%s] has no stream converter", key)
		}
		sr, ok := v.(streamReader)
		if !ok {
			return fmt.Errorf("checkpoint conv stream fail, value of [%s] isn't stream", key)
		}
		nValue, err := convPair.concatStream(sr, ignoreError)
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
		if convPair.restoreStream == nil {
			return fmt.Errorf("checkpoint restore stream fail, node[%s] has no stream converter", key)
		}
		sr, err := convPair.restoreStream(v)
		if err != nil {
			return err
		}
		values[key] = sr
	}
	return nil
}
