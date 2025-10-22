package core

import "context"

// GetInterruptState provides a type-safe way to check for and retrieve the persisted state from a previous interruption.
// It is the primary function a component should use to understand its past state.
//
// It returns three values:
//   - wasInterrupted (bool): True if the node was part of a previous interruption, regardless of whether state was provided.
//   - state (T): The typed state object, if it was provided and matches type `T`.
//   - hasState (bool): True if state was provided during the original interrupt and successfully cast to type `T`.
func GetInterruptState[T any](ctx context.Context) (wasInterrupted bool, hasState bool, state T) {
	rCtx, ok := getRunCtx(ctx)
	if !ok || rCtx.interruptState == nil {
		return
	}

	wasInterrupted = true
	if rCtx.interruptState.State == nil {
		return
	}

	state, hasState = rCtx.interruptState.State.(T)
	return
}

// GetResumeContext checks if the current component is the target of a resume operation
// and retrieves any data provided by the user for that resumption.
//
// This function is typically called *after* a component has already determined it is in a
// resumed state by calling GetInterruptState.
//
// It returns three values:
//   - isResumeFlow: A boolean that is true if the current component's address was explicitly targeted
//     by a call to Resume() or ResumeWithData().
//   - hasData: A boolean that is true if data was provided for this component (i.e., not nil).
//   - data: The typed data provided by the user.
//
// ### How to Use This Function: A Decision Framework
//
// The correct usage pattern depends on the application's desired resume strategy.
//
// #### Strategy 1: Implicit "Resume All"
// In some use cases, any resume operation implies that *all* interrupted points should proceed.
// For example, if an application's UI only provides a single "Continue" button for a set of
// interruptions. In this model, a component can often just use `GetInterruptState` to see if
// `wasInterrupted` is true and then proceed with its logic, as it can assume it is an intended target.
// It may still call `GetResumeContext` to check for optional data, but the `isResumeFlow` flag is less critical.
//
// #### Strategy 2: Explicit "Targeted Resume" (Most Common)
// For applications with multiple, distinct interrupt points that must be resumed independently, it is
// crucial to differentiate which point is being resumed. This is the primary use case for the `isResumeFlow` flag.
//   - If `isResumeFlow` is `true`: Your component is the explicit target. You should consume
//     the `data` (if any) and complete your work.
//   - If `isResumeFlow` is `false`: Another component is the target. You MUST re-interrupt
//     (e.g., by returning `StatefulInterrupt(...)`) to preserve your state and allow the
//     resume signal to propagate.
//
// ### Guidance for Composite Components
//
// Composite components (like `Graph` or other `Runnable`s that contain sub-processes) have a dual role:
//  1. Check for Self-Targeting: A composite component can itself be the target of a resume
//     operation, for instance, to modify its internal state. It may call `GetResumeContext`
//     to check for data targeted at its own address.
//  2. Act as a Conduit: After checking for itself, its primary role is to re-execute its children,
//     allowing the resume context to flow down to them. It must not consume a resume signal
//     intended for one of its descendants.
func GetResumeContext[T any](ctx context.Context) (isResumeTarget bool, hasData bool, data T) {
	rCtx, ok := getRunCtx(ctx)
	if !ok {
		return
	}

	isResumeTarget = rCtx.isResumeTarget
	if !isResumeTarget {
		return
	}

	// It is a resume flow, now check for data
	if rCtx.resumeData == nil {
		return // hasData is false
	}

	data, hasData = rCtx.resumeData.(T)
	return
}

// GetAllResumeData retrieves all resume data from the context that has not yet been claimed and used
// by a resumed component.
//
// This function is primarily intended for advanced use cases involving "bridge" components that
// contain nested, independent execution environments (e.g., an adk.AgentTool running an
// adk.Agent which in turn contains a compose.Graph).
//
// The typical pattern for such a bridge component during a resume operation is:
// 1. Call GetAllResumeData to get the complete map of unused data.
// 2. Identify and consume the data intended for the bridge component itself, often by deleting it from the map.
// 3. Pass the remaining data down to the nested environment using its own BatchResumeWithData function.
// This ensures that resume data is correctly routed across different framework layers.
func GetAllResumeData(ctx context.Context) map[string]any {
	rInfo, ok := ctx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
	if !ok || rInfo == nil {
		return nil
	}

	result := make(map[string]any, len(rInfo.id2ResumeData))
	// Copy all resume data to the result map
	for id, data := range rInfo.id2ResumeData {
		result[id] = data
	}

	for id := range rInfo.id2ResumeDataUsed {
		delete(result, id)
	}

	return result
}

func getRunCtx(ctx context.Context) (*addrCtx, bool) {
	rCtx, ok := ctx.Value(addrCtxKey{}).(*addrCtx)
	return rCtx, ok
}
