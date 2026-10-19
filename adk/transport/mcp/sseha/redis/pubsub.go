/*
 * Copyright 2025 CloudWeGo Authors
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

package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk/transport/mcp/sseha"
)

// EventBusConfig configures the Redis pub/sub-based event bus.
type EventBusConfig struct {
	// Client is the Redis client to use. Note: some Redis client libraries
	// require a separate connection for pub/sub. The caller is responsible
	// for providing an appropriate client.
	Client Client

	// ChannelPrefix is prepended to all pub/sub channel names.
	// Default: "eino:sseha:events:"
	ChannelPrefix string

	// AllEventsChannel is the channel name for the wildcard subscription.
	// Default: "eino:sseha:events:*"
	AllEventsChannel string

	// BufferSize is the channel buffer size for subscription channels.
	// Default: 256
	BufferSize int
}

// DefaultEventBusConfig returns sensible defaults.
func DefaultEventBusConfig() *EventBusConfig {
	return &EventBusConfig{
		ChannelPrefix:    "eino:sseha:events:",
		AllEventsChannel: "eino:sseha:events:*",
		BufferSize:       256,
	}
}

// EventBus implements sseha.EventBus using Redis Pub/Sub.
type EventBus struct {
	client           Client
	channelPrefix    string
	allEventsChannel string
	bufferSize       int

	mu            sync.RWMutex
	subscriptions map[string]*eventBusSubscription
	closed        bool
}

// Verify interface compliance at compile time.
var _ sseha.EventBus = (*EventBus)(nil)

type eventBusSubscription struct {
	sessionID  string
	ch         chan *sseha.SSEEvent
	redisSub   Subscription
	cancelFunc context.CancelFunc
}

// NewEventBus creates a new Redis pub/sub-based event bus.
func NewEventBus(config *EventBusConfig) *EventBus {
	if config == nil {
		config = DefaultEventBusConfig()
	}
	if config.ChannelPrefix == "" {
		config.ChannelPrefix = "eino:sseha:events:"
	}
	if config.AllEventsChannel == "" {
		config.AllEventsChannel = "eino:sseha:events:*"
	}
	if config.BufferSize <= 0 {
		config.BufferSize = 256
	}

	return &EventBus{
		client:           config.Client,
		channelPrefix:    config.ChannelPrefix,
		allEventsChannel: config.AllEventsChannel,
		bufferSize:       config.BufferSize,
		subscriptions:    make(map[string]*eventBusSubscription),
	}
}

func (b *EventBus) channelName(sessionID string) string {
	return fmt.Sprintf("%s%s", b.channelPrefix, sessionID)
}

// Publish sends an SSE event to the event bus via Redis PUBLISH.
func (b *EventBus) Publish(ctx context.Context, event *sseha.SSEEvent) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return sseha.ErrManagerClosed
	}
	b.mu.RUnlock()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	channel := b.channelName(event.SessionID)
	if err := b.client.Publish(ctx, channel, string(data)); err != nil {
		return fmt.Errorf("redis publish: %w", err)
	}

	return nil
}

// Subscribe creates a subscription for events of a specific session.
func (b *EventBus) Subscribe(ctx context.Context, sessionID string) (<-chan *sseha.SSEEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, sseha.ErrManagerClosed
	}

	if _, exists := b.subscriptions[sessionID]; exists {
		return nil, sseha.ErrSubscriptionExists
	}

	channel := b.channelName(sessionID)
	subCtx, cancel := context.WithCancel(ctx)

	redisSub, err := b.client.Subscribe(subCtx, channel)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("redis subscribe: %w", err)
	}

	eventCh := make(chan *sseha.SSEEvent, b.bufferSize)

	sub := &eventBusSubscription{
		sessionID:  sessionID,
		ch:         eventCh,
		redisSub:   redisSub,
		cancelFunc: cancel,
	}

	b.subscriptions[sessionID] = sub

	// Start goroutine to forward Redis messages to the event channel
	go b.forwardMessages(subCtx, sub)

	return eventCh, nil
}

// forwardMessages reads from the Redis subscription and forwards to the event channel.
func (b *EventBus) forwardMessages(ctx context.Context, sub *eventBusSubscription) {
	defer close(sub.ch)

	redisCh := sub.redisSub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-redisCh:
			if !ok {
				return
			}

			var event sseha.SSEEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				continue // skip malformed messages
			}

			select {
			case sub.ch <- &event:
			case <-ctx.Done():
				return
			default:
				// Channel full — drop the oldest event to make room
				select {
				case <-sub.ch:
				default:
				}
				sub.ch <- &event
			}
		}
	}
}

// Unsubscribe removes a subscription for a specific session.
func (b *EventBus) Unsubscribe(ctx context.Context, sessionID string) error {
	b.mu.Lock()
	sub, exists := b.subscriptions[sessionID]
	if exists {
		delete(b.subscriptions, sessionID)
	}
	b.mu.Unlock()

	if !exists {
		return nil
	}

	sub.cancelFunc()
	_ = sub.redisSub.Unsubscribe()

	return nil
}

// SubscribeAll creates a subscription for all session events using pattern matching.
func (b *EventBus) SubscribeAll(ctx context.Context) (<-chan *sseha.SSEEvent, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, sseha.ErrManagerClosed
	}

	const allKey = "__all__"
	if _, exists := b.subscriptions[allKey]; exists {
		return nil, sseha.ErrSubscriptionExists
	}

	subCtx, cancel := context.WithCancel(ctx)

	redisSub, err := b.client.PSubscribe(subCtx, b.allEventsChannel)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("redis psubscribe: %w", err)
	}

	eventCh := make(chan *sseha.SSEEvent, b.bufferSize)

	sub := &eventBusSubscription{
		sessionID:  allKey,
		ch:         eventCh,
		redisSub:   redisSub,
		cancelFunc: cancel,
	}

	b.subscriptions[allKey] = sub

	go b.forwardMessages(subCtx, sub)

	return eventCh, nil
}

// Close releases all resources and closes all subscriptions.
func (b *EventBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}
	b.closed = true

	for _, sub := range b.subscriptions {
		sub.cancelFunc()
		_ = sub.redisSub.Unsubscribe()
	}
	b.subscriptions = nil

	return nil
}
