package core

import (
	"context"
	"errors"
	"fmt"
)

type CheckPointStore interface {
	Get(ctx context.Context, checkPointID string) ([]byte, bool, error)
	Set(ctx context.Context, checkPointID string, checkPoint []byte) error
}

type inMemoryStore struct {
	Data  []byte
	Valid bool
}

func (m *inMemoryStore) Get(_ context.Context, _ string) ([]byte, bool, error) {
	if m.Valid {
		return m.Data, true, nil
	}
	return nil, false, nil
}

func (m *inMemoryStore) Set(_ context.Context, _ string, checkPoint []byte) error {
	m.Data = checkPoint
	m.Valid = true
	return nil
}

func newEmptyStore() CheckPointStore {
	return &inMemoryStore{}
}

func newResumeStore(data []byte) CheckPointStore {
	return &inMemoryStore{
		Data:  data,
		Valid: true,
	}
}

type InterruptSignal struct {
	ID string
	Address
	InterruptInfo
	InterruptState
	Subs []*InterruptSignal
}

func (is *InterruptSignal) Error() string {
	return fmt.Sprintf("interrupt signal: ID=%s, Addr=%s, Info=%s, State=%s, SubsLen=%d",
		is.ID, is.Address.String(), is.InterruptInfo.String(), is.InterruptState.String(), len(is.Subs))
}

type InterruptState struct {
	State                any
	LayerSpecificPayload any
}

func (is *InterruptState) String() string {
	if is == nil {
		return ""
	}
	return fmt.Sprintf("interrupt state: State=%v, LayerSpecificPayload=%v", is.State, is.LayerSpecificPayload)
}

// InterruptConfig holds optional parameters for creating an interrupt.
type InterruptConfig struct {
	LayerPayload any
}

// InterruptOption is a function that configures an InterruptConfig.
type InterruptOption func(*InterruptConfig)

// WithLayerPayload creates an option to attach layer-specific metadata
// to the interrupt's state.
func WithLayerPayload(payload any) InterruptOption {
	return func(c *InterruptConfig) {
		c.LayerPayload = payload
	}
}

func Interrupt(ctx context.Context, info any, state any, subContexts []*InterruptSignal, opts ...InterruptOption) (
	*InterruptSignal, error) {
	addr, exist := GetCurrentAddress(ctx)
	if !exist {
		return nil, errors.New("address not exist")
	}

	// Apply options to get config
	config := &InterruptConfig{}
	for _, opt := range opts {
		opt(config)
	}

	myPoint := InterruptInfo{
		Info: info,
	}

	if len(subContexts) == 0 {
		myPoint.IsRootCause = true
		return &InterruptSignal{
			ID:            addr.String(),
			Address:       addr,
			InterruptInfo: myPoint,
			InterruptState: InterruptState{
				State:                state,
				LayerSpecificPayload: config.LayerPayload,
			},
		}, nil
	}

	return &InterruptSignal{
		ID:            addr.String(),
		Address:       addr,
		InterruptInfo: myPoint,
		InterruptState: InterruptState{
			State:                state,
			LayerSpecificPayload: config.LayerPayload,
		},
		Subs: subContexts,
	}, nil
}

// InterruptCtx provides a complete, user-facing context for a single, resumable interrupt point.
type InterruptCtx struct {
	// ID is the unique, fully-qualified address of the interrupt point.
	// It is constructed by joining the individual Address segments, e.g., "agent:A;node:graph_a;tool:tool_call_123".
	// This ID should be used when providing resume data via ResumeWithData.
	ID string
	// Address is the structured sequence of AddressSegment segments that leads to the interrupt point.
	Address Address
	// Info is the user-facing information associated with the interrupt, provided by the component that triggered it.
	Info any
	// IsRootCause indicates whether the interrupt point is the exact root cause for an interruption.
	IsRootCause bool
	// Parent points to the context of the parent component in the interrupt chain.
	// It is nil for the top-level interrupt.
	Parent *InterruptCtx
}

// FromInterruptContexts converts a list of user-facing InterruptCtx objects into an
// internal InterruptSignal tree. It correctly handles common ancestors and ensures
// that the resulting tree is consistent with the original interrupt chain.
//
// This method is primarily used by components that bridge different execution environments.
// For example, an `adk.AgentTool` might catch an `adk.InterruptInfo`, extract the
// `adk.InterruptCtx` objects from it, and then call this method on each one. The resulting
// error signals are then typically aggregated into a single error using `compose.CompositeInterrupt`
// to be returned from the tool's `InvokableRun` method.
// FromInterruptContexts reconstructs a single InterruptSignal tree from a list of
// user-facing InterruptCtx objects. It correctly merges common ancestors.
func FromInterruptContexts(contexts []*InterruptCtx) *InterruptSignal {
	if len(contexts) == 0 {
		return nil
	}

	signalMap := make(map[string]*InterruptSignal)
	var rootSignal *InterruptSignal

	// getOrCreateSignal is a recursive helper that builds the tree bottom-up.
	var getOrCreateSignal func(*InterruptCtx) *InterruptSignal
	getOrCreateSignal = func(ctx *InterruptCtx) *InterruptSignal {
		if ctx == nil {
			return nil
		}
		// If we've already created a signal for this context, return it.
		if signal, exists := signalMap[ctx.ID]; exists {
			return signal
		}

		// Create the signal for the current context.
		newSignal := &InterruptSignal{
			ID:      ctx.ID,
			Address: ctx.Address,
			InterruptInfo: InterruptInfo{
				Info:        ctx.Info,
				IsRootCause: ctx.IsRootCause,
			},
		}
		signalMap[ctx.ID] = newSignal // Cache it immediately.

		// Recursively ensure the parent exists. If it doesn't, this is the root.
		if parentSignal := getOrCreateSignal(ctx.Parent); parentSignal != nil {
			parentSignal.Subs = append(parentSignal.Subs, newSignal)
		} else {
			rootSignal = newSignal
		}
		return newSignal
	}

	// Process all contexts to ensure all branches of the tree are built.
	for _, ctx := range contexts {
		_ = getOrCreateSignal(ctx)
	}

	return rootSignal
}

// ToInterruptContexts converts the internal InterruptSignal tree into a list of
// user-facing InterruptCtx objects for the root causes of the interruption.
// Each returned context has its Parent field populated (if it has a parent),
// allowing traversal up the interrupt chain.
func ToInterruptContexts(is *InterruptSignal) []*InterruptCtx {
	if is == nil {
		return nil
	}
	var rootCauseContexts []*InterruptCtx

	// A recursive helper that traverses the signal tree, building the parent-linked
	// context objects and appending only the root causes to the final list.
	var buildContexts func(*InterruptSignal, *InterruptCtx)
	buildContexts = func(signal *InterruptSignal, parentCtx *InterruptCtx) {
		currentCtx := &InterruptCtx{
			ID:          signal.ID,
			Address:     signal.Address,
			Info:        signal.InterruptInfo.Info,
			IsRootCause: signal.InterruptInfo.IsRootCause,
			Parent:      parentCtx,
		}

		// Only add the context to the final list if it's a root cause.
		if currentCtx.IsRootCause {
			rootCauseContexts = append(rootCauseContexts, currentCtx)
		}

		// Recurse into children, passing the newly created context as their parent.
		for _, subSignal := range signal.Subs {
			buildContexts(subSignal, currentCtx)
		}
	}

	buildContexts(is, nil)
	return rootCauseContexts
}

// SignalToPersistenceMaps flattens an InterruptSignal tree into two maps suitable for persistence in a checkpoint.
func SignalToPersistenceMaps(is *InterruptSignal) (map[string]Address, map[string]InterruptState) {
	id2addr := make(map[string]Address)
	id2state := make(map[string]InterruptState)

	if is == nil {
		return id2addr, id2state
	}

	var traverse func(*InterruptSignal)
	traverse = func(signal *InterruptSignal) {
		// Add current signal's data to the maps.
		id2addr[signal.ID] = signal.Address
		id2state[signal.ID] = signal.InterruptState // The embedded struct

		// Recurse into children.
		for _, sub := range signal.Subs {
			traverse(sub)
		}
	}

	traverse(is)
	return id2addr, id2state
}
