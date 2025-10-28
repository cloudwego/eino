package core

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/internal/generic"
)

// AddressSegmentType defines the type of a segment in an execution address.
type AddressSegmentType string

// Address represents a full, hierarchical address to a point in the execution structure.
type Address []AddressSegment

// String converts an Address into its unique string representation.
func (p Address) String() string {
	if p == nil {
		return ""
	}
	var sb strings.Builder
	for i, s := range p {
		sb.WriteString(string(s.Type))
		sb.WriteString(":")
		sb.WriteString(s.ID)
		if i != len(p)-1 {
			sb.WriteString(";")
		}
	}
	return sb.String()
}

func (p Address) Equals(other Address) bool {
	if len(p) != len(other) {
		return false
	}
	for i := range p {
		if p[i].Type != other[i].Type || p[i].ID != other[i].ID {
			return false
		}
	}
	return true
}

// HasPrefix checks if the address begins with the given prefix.
func (p Address) HasPrefix(prefix Address) bool {
	if len(p) < len(prefix) {
		return false
	}
	for i := range prefix {
		if p[i] != prefix[i] {
			return false
		}
	}
	return true
}

func (p Address) DeepCopy() Address {
	if p == nil {
		return nil
	}
	cpy := make(Address, len(p))
	copy(cpy, p)
	return cpy
}

// AddressSegment represents a single segment in the hierarchical address of an execution point.
// A sequence of AddressSegments uniquely identifies a location within a potentially nested structure.
type AddressSegment struct {
	// ID is the unique identifier for this segment, e.g., the node's key or the tool call's ID.
	ID string
	// Type indicates whether this address segment is a graph node, a tool call, an agent, etc.
	Type AddressSegmentType
}

type addrCtxKey struct{}

type addrCtx struct {
	addr           Address
	interruptState *InterruptState
	isResumeTarget bool
	resumeData     any
}

type globalResumeInfoKey struct{}

type globalResumeInfo struct {
	mu                sync.Mutex
	id2ResumeData     map[string]any
	id2ResumeDataUsed map[string]bool
	id2State          map[string]InterruptState
	id2StateUsed      map[string]bool
	id2Addr           map[string]Address
}

// GetCurrentAddress returns the hierarchical address of the currently executing component.
// The address is a sequence of segments, each identifying a structural part of the execution
// like an agent, a graph node, or a tool call. This can be useful for logging or debugging.
func GetCurrentAddress(ctx context.Context) Address {
	if p, ok := ctx.Value(addrCtxKey{}).(*addrCtx); ok {
		return p.addr
	}

	return nil
}

// AppendAddressSegment creates a new execution context for a sub-component (e.g., a graph node or a tool call).
//
// It extends the current context's address with a new segment and populates the new context with the
// appropriate interrupt state and resume data for that specific sub-address.
//
//   - ctx: The parent context, typically the one passed into the component's Invoke/Stream method.
//   - segType: The type of the new address segment (e.g., "node", "tool").
//   - segID: The unique ID for the new address segment.
func AppendAddressSegment(ctx context.Context, segType AddressSegmentType, segID string) context.Context {
	// get current address
	currentAddress := GetCurrentAddress(ctx)
	if len(currentAddress) == 0 {
		currentAddress = []AddressSegment{
			{
				Type: segType,
				ID:   segID,
			},
		}
	} else {
		newAddress := make([]AddressSegment, len(currentAddress)+1)
		copy(newAddress, currentAddress)
		newAddress[len(newAddress)-1] = AddressSegment{
			Type: segType,
			ID:   segID,
		}
		currentAddress = newAddress
	}

	runCtx := &addrCtx{
		addr: currentAddress,
	}

	rInfo, hasRInfo := getResumeInfo(ctx)
	if !hasRInfo {
		return context.WithValue(ctx, addrCtxKey{}, runCtx)
	}

	for id, addr := range rInfo.id2Addr {
		if addr.Equals(currentAddress) {
			if used, ok := rInfo.id2StateUsed[id]; !ok || !used {
				runCtx.interruptState = generic.PtrOf(rInfo.id2State[id])
				rInfo.id2StateUsed[id] = true
				break
			}
		}
	}

	// take from globalResumeInfo the data for the new address if there is any
	id := currentAddress.String()
	rInfo.mu.Lock()
	defer rInfo.mu.Unlock()
	used := rInfo.id2ResumeDataUsed[id]
	if !used {
		rData, existed := rInfo.id2ResumeData[id]
		if existed {
			rInfo.id2ResumeDataUsed[id] = true
			runCtx.resumeData = rData
			runCtx.isResumeTarget = true
		}
	}

	return context.WithValue(ctx, addrCtxKey{}, runCtx)
}

// Equal checks if two Addresses are identical.
func (p Address) Equal(other Address) bool {
	if len(p) != len(other) {
		return false
	}
	for i := range p {
		if p[i] != other[i] {
			return false
		}
	}
	return true
}

// GetNextResumptionPoints finds the immediate child resumption points for a given parent address.
func GetNextResumptionPoints(ctx context.Context) (map[string]bool, error) {
	parentAddr := GetCurrentAddress(ctx)

	rInfo, exists := getResumeInfo(ctx)
	if !exists {
		return nil, fmt.Errorf("GetNextResumptionPoints: failed to get resume info from context")
	}

	nextPoints := make(map[string]bool)
	parentAddrLen := len(parentAddr)

	for _, addr := range rInfo.id2Addr {
		// Check if addr is a potential child (must be longer than parent)
		if len(addr) <= parentAddrLen {
			continue
		}

		// Check if it has the parent address as a prefix
		var isPrefix bool
		if parentAddrLen == 0 {
			isPrefix = true
		} else {
			isPrefix = addr[:parentAddrLen].Equal(parentAddr)
		}

		if !isPrefix {
			continue
		}

		// We are looking for immediate children.
		// The address of an immediate child should be one segment longer.
		childAddr := addr[parentAddrLen : parentAddrLen+1]
		childID := childAddr[0].ID

		// Avoid adding duplicates.
		if _, ok := nextPoints[childID]; !ok {
			nextPoints[childID] = true
		}
	}

	return nextPoints, nil
}

// SetParentAddress returns a new context that contains the given parent address.
// This is used to bridge the address hierarchy between different execution layers,
// such as between a parent package (such as adk package) and the compose package.
// It's important to note that this function will overwrite any previous address
// that may exist in the context.
func SetParentAddress(ctx context.Context, addr Address) context.Context {
	if addr == nil {
		return ctx
	}

	runCtx := &addrCtx{
		addr: addr.DeepCopy(),
	}

	return context.WithValue(ctx, addrCtxKey{}, runCtx)
}

// BatchResumeWithData is the core function for preparing a resume context. It injects a map
// of resume targets and their corresponding data into the context.
//
// The `resumeData` map should contain the interrupt IDs (which are the string form of addresses) of the
// components to be resumed as keys. The value can be the resume data for that component, or `nil`
// if no data is needed (equivalent to using `Resume`).
//
// This function is the foundation for the "Explicit Targeted Resume" strategy. Components whose interrupt IDs
// are present as keys in the map will receive `isResumeFlow = true` when they call `GetResumeContext`.
func BatchResumeWithData(ctx context.Context, resumeData map[string]any) context.Context {
	rInfo, ok := ctx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
	if !ok {
		// Create a new globalResumeInfo and copy the map to prevent external mutation.
		newMap := make(map[string]any, len(resumeData))
		for k, v := range resumeData {
			newMap[k] = v
		}
		return context.WithValue(ctx, globalResumeInfoKey{}, &globalResumeInfo{
			id2ResumeData:     newMap,
			id2ResumeDataUsed: make(map[string]bool),
			id2StateUsed:      make(map[string]bool),
		})
	}

	rInfo.mu.Lock()
	defer rInfo.mu.Unlock()
	if rInfo.id2ResumeData == nil {
		rInfo.id2ResumeData = make(map[string]any)
	}
	for id, data := range resumeData {
		rInfo.id2ResumeData[id] = data
	}
	return ctx
}

func PopulateInterruptState(ctx context.Context, id2Addr map[string]Address,
	id2State map[string]InterruptState) context.Context {
	rInfo, ok := ctx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
	if ok {
		if rInfo.id2Addr == nil {
			rInfo.id2Addr = make(map[string]Address)
		}
		for id, addr := range id2Addr {
			rInfo.id2Addr[id] = addr
		}
		rInfo.id2State = id2State
	} else {
		rInfo = &globalResumeInfo{
			id2Addr:           id2Addr,
			id2State:          id2State,
			id2StateUsed:      make(map[string]bool),
			id2ResumeDataUsed: make(map[string]bool),
		}
		ctx = context.WithValue(ctx, globalResumeInfoKey{}, rInfo)
	}
	return ctx
}

func getResumeInfo(ctx context.Context) (*globalResumeInfo, bool) {
	info, ok := ctx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
	return info, ok
}

type InterruptInfo struct {
	Info        any
	IsRootCause bool
}

func (i *InterruptInfo) String() string {
	if i == nil {
		return ""
	}
	return fmt.Sprintf("interrupt info: Info=%v, IsRootCause=%v", i.Info, i.IsRootCause)
}
