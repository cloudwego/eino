package adk

import "context"

type InterruptInfo struct {
	Data any
}

type CheckPointStore interface {
	Set(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, bool, error)
}

type checkpoint struct {
	AgentName string

	Events []*AgentEvent
	Values map[string]any
}

func getCheckPoint(ctx context.Context, store CheckPointStore, key string) (*runContext, *InterruptInfo, bool, error) {
	return nil, nil, false, nil
}

func saveCheckPoint(
	ctx context.Context,
	store CheckPointStore,
	key string,
	runCtx *runContext,
	info *InterruptInfo) error {
	return nil
}
