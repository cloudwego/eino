package team

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/adk"
)

type stubTurnInputSource struct {
	items []TurnInput
	err   error
}

func (s *stubTurnInputSource) Receive(ctx context.Context, _ adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {
	if len(s.items) == 0 {
		if s.err != nil {
			return ctx, TurnInput{}, nil, s.err
		}
		return ctx, TurnInput{}, nil, adk.ErrLoopExit
	}
	item := s.items[0]
	s.items = s.items[1:]
	return ctx, item, nil, nil
}

func (s *stubTurnInputSource) Front(ctx context.Context, cfg adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {
	return s.Receive(ctx, cfg)
}

func TestSourceRouterBuffersForOtherAgent(t *testing.T) {
	ctx := context.Background()
	router := newSourceRouter(&stubTurnInputSource{
		items: []TurnInput{
			{TargetAgent: "worker", Messages: []string{"task"}},
		},
	}, LeaderAgentName)

	router.SourceFor(LeaderAgentName)
	router.SourceFor("worker")

	_, leaderItem, _, err := router.receiveFor(ctx, LeaderAgentName)
	if !errors.Is(err, adk.ErrLoopExit) {
		t.Fatalf("expected leader to exit after upstream drains, got item=%+v err=%v", leaderItem, err)
	}

	_, workerItem, _, err := router.receiveFor(ctx, "worker")
	if err != nil {
		t.Fatalf("worker receive: %v", err)
	}
	if workerItem.TargetAgent != "worker" || len(workerItem.Messages) != 1 || workerItem.Messages[0] != "task" {
		t.Fatalf("unexpected worker item: %+v", workerItem)
	}
}

func TestSourceRouterFallsBackToDefaultAgent(t *testing.T) {
	ctx := context.Background()
	router := newSourceRouter(&stubTurnInputSource{
		items: []TurnInput{
			{TargetAgent: "", Messages: []string{"leader"}},
			{TargetAgent: "unknown", Messages: []string{"fallback"}},
		},
	}, LeaderAgentName)

	router.SourceFor(LeaderAgentName)

	_, first, _, err := router.receiveFor(ctx, LeaderAgentName)
	if err != nil {
		t.Fatalf("leader first receive: %v", err)
	}
	if first.TargetAgent != "" || first.Messages[0] != "leader" {
		t.Fatalf("unexpected first item: %+v", first)
	}

	_, second, _, err := router.receiveFor(ctx, LeaderAgentName)
	if err != nil {
		t.Fatalf("leader second receive: %v", err)
	}
	if second.TargetAgent != "unknown" || second.Messages[0] != "fallback" {
		t.Fatalf("unexpected fallback item: %+v", second)
	}
}
