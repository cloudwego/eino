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
//   - isResumeTarget: A boolean that is true if the current component's execution path was explicitly targeted
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
// It may still call `GetResumeContext` to check for optional data, but the `isResumeTarget` flag is less critical.
//
// #### Strategy 2: Explicit "Targeted Resume" (Most Common)
// For applications with multiple, distinct interrupt points that must be resumed independently, it is
// crucial to differentiate which point is being resumed. This is the primary use case for the `isResumeTarget` flag.
//   - If `isResumeTarget` is `true`: Your component is the explicit target. You should consume
//     the `data` (if any) and complete your work.
//   - If `isResumeTarget` is `false`: Another component is the target. You MUST re-interrupt
//     (e.g., by returning `StatefulInterrupt(...)`) to preserve your state and allow the
//     resume signal to propagate.
//
// ### Guidance for Composite Components
//
// Composite components (like `Graph` or other `Runnable`s that contain sub-processes) have a dual role:
//  1. Check for Self-Targeting: A composite component can itself be the target of a resume
//     operation, for instance, to modify its internal state. It may call `GetResumeContext`
//     to check for data targeted at its own execution path.
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

func getRunCtx(ctx context.Context) (*pathCtx, bool) {
	rCtx, ok := ctx.Value(pathCtxKey{}).(*pathCtx)
	return rCtx, ok
}
