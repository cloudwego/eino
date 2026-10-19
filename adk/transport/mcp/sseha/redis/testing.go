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
	"time"

	"github.com/cloudwego/eino/adk/transport/mcp/sseha"
)

// InMemoryClient provides an in-memory implementation of Client for testing
// purposes. It simulates Redis commands without requiring an actual Redis server.
//
// Usage in tests:
//
//	client := redis.NewInMemoryClient()
//	store := redis.NewMetadataStore(&redis.MetadataStoreConfig{Client: client})
//	bus := redis.NewEventBus(&redis.EventBusConfig{Client: client})
type InMemoryClient struct {
	mu sync.RWMutex

	// data stores key-value pairs
	data map[string]string

	// ttls stores expiry times
	ttls map[string]time.Time

	// sets stores set members
	sets map[string]map[string]bool

	// pubsub
	pubsubMu     sync.RWMutex
	subscribers  map[string][]chan Message
	psubscribers map[string][]chan Message
}

// Verify interface compliance at compile time.
var _ Client = (*InMemoryClient)(nil)

// NewInMemoryClient creates a new in-memory Redis client for testing.
func NewInMemoryClient() *InMemoryClient {
	return &InMemoryClient{
		data:         make(map[string]string),
		ttls:         make(map[string]time.Time),
		sets:         make(map[string]map[string]bool),
		subscribers:  make(map[string][]chan Message),
		psubscribers: make(map[string][]chan Message),
	}
}

func (c *InMemoryClient) isExpired(key string) bool {
	if exp, ok := c.ttls[key]; ok {
		if time.Now().After(exp) {
			delete(c.data, key)
			delete(c.ttls, key)
			return true
		}
	}
	return false
}

// Get retrieves the value for a key.
func (c *InMemoryClient) Get(ctx context.Context, key string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.isExpired(key) {
		return "", nil
	}

	val, ok := c.data[key]
	if !ok {
		return "", nil
	}
	return val, nil
}

// Set stores a key-value pair with an optional TTL.
func (c *InMemoryClient) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[key] = value
	if ttl > 0 {
		c.ttls[key] = time.Now().Add(ttl)
	} else {
		delete(c.ttls, key)
	}
	return nil
}

// Del deletes one or more keys.
func (c *InMemoryClient) Del(ctx context.Context, keys ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, key := range keys {
		delete(c.data, key)
		delete(c.ttls, key)
	}
	return nil
}

// SetNX sets the value only if the key does not exist.
func (c *InMemoryClient) SetNX(ctx context.Context, key string, value string, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isExpired(key) {
		// key expired, treat as not existing
	} else if _, exists := c.data[key]; exists {
		return false, nil
	}

	c.data[key] = value
	if ttl > 0 {
		c.ttls[key] = time.Now().Add(ttl)
	}
	return true, nil
}

// Eval executes a simulated Lua script.
func (c *InMemoryClient) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Handle the updateSession Lua script
	if len(keys) == 1 && len(args) == 3 {
		key := keys[0]
		expectedVersion, _ := toInt64(args[0])
		newData, _ := args[1].(string)
		ttlSeconds, _ := toInt64(args[2])

		current, exists := c.data[key]
		if !exists || c.isExpired(key) {
			return nil, fmt.Errorf("session_not_found")
		}

		var info sseha.SessionInfo
		if err := json.Unmarshal([]byte(current), &info); err != nil {
			return nil, fmt.Errorf("unmarshal current: %w", err)
		}

		if info.Version != expectedVersion {
			return nil, fmt.Errorf("version_conflict")
		}

		c.data[key] = newData
		if ttlSeconds > 0 {
			c.ttls[key] = time.Now().Add(time.Duration(ttlSeconds) * time.Second)
		}

		return int64(1), nil
	}

	// Handle the releaseLock Lua script
	if len(keys) == 1 && len(args) == 1 {
		key := keys[0]
		expectedNode, _ := args[0].(string)

		current, exists := c.data[key]
		if exists && current == expectedNode {
			delete(c.data, key)
			delete(c.ttls, key)
			return int64(1), nil
		}
		return int64(0), nil
	}

	return nil, fmt.Errorf("unsupported lua script")
}

func toInt64(v any) (int64, bool) {
	switch val := v.(type) {
	case int64:
		return val, true
	case int:
		return int64(val), true
	case float64:
		return int64(val), true
	default:
		return 0, false
	}
}

// SMembers returns all members of a set.
func (c *InMemoryClient) SMembers(ctx context.Context, key string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	s, ok := c.sets[key]
	if !ok {
		return nil, nil
	}
	result := make([]string, 0, len(s))
	for member := range s {
		result = append(result, member)
	}
	return result, nil
}

// SAdd adds members to a set.
func (c *InMemoryClient) SAdd(ctx context.Context, key string, members ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.sets[key]; !ok {
		c.sets[key] = make(map[string]bool)
	}
	for _, m := range members {
		c.sets[key][fmt.Sprintf("%v", m)] = true
	}
	return nil
}

// SRem removes members from a set.
func (c *InMemoryClient) SRem(ctx context.Context, key string, members ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s, ok := c.sets[key]; ok {
		for _, m := range members {
			delete(s, fmt.Sprintf("%v", m))
		}
	}
	return nil
}

// Publish publishes a message to a pub/sub channel.
func (c *InMemoryClient) Publish(ctx context.Context, channel string, message string) error {
	c.pubsubMu.RLock()
	defer c.pubsubMu.RUnlock()

	// Send to exact subscribers
	for _, ch := range c.subscribers[channel] {
		select {
		case ch <- Message{Channel: channel, Payload: message}:
		default:
			// Drop if channel is full
		}
	}

	// Send to pattern subscribers (simple prefix match for testing)
	for pattern, subs := range c.psubscribers {
		if matchPattern(pattern, channel) {
			for _, ch := range subs {
				select {
				case ch <- Message{Channel: channel, Payload: message}:
				default:
				}
			}
		}
	}

	return nil
}

// matchPattern implements simple glob matching for testing (just * at end).
func matchPattern(pattern, channel string) bool {
	if len(pattern) == 0 {
		return len(channel) == 0
	}
	if pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return len(channel) >= len(prefix) && channel[:len(prefix)] == prefix
	}
	return pattern == channel
}

// Subscribe subscribes to pub/sub channels.
func (c *InMemoryClient) Subscribe(ctx context.Context, channels ...string) (Subscription, error) {
	c.pubsubMu.Lock()
	defer c.pubsubMu.Unlock()

	ch := make(chan Message, 256)
	for _, channel := range channels {
		c.subscribers[channel] = append(c.subscribers[channel], ch)
	}

	return &inMemorySubscription{
		client:   c,
		ch:       ch,
		channels: channels,
		pattern:  false,
	}, nil
}

// PSubscribe subscribes to pub/sub channels using pattern matching.
func (c *InMemoryClient) PSubscribe(ctx context.Context, patterns ...string) (Subscription, error) {
	c.pubsubMu.Lock()
	defer c.pubsubMu.Unlock()

	ch := make(chan Message, 256)
	for _, pattern := range patterns {
		c.psubscribers[pattern] = append(c.psubscribers[pattern], ch)
	}

	return &inMemorySubscription{
		client:   c,
		ch:       ch,
		channels: patterns,
		pattern:  true,
	}, nil
}

// Close closes the in-memory client.
func (c *InMemoryClient) Close() error {
	return nil
}

type inMemorySubscription struct {
	client   *InMemoryClient
	ch       chan Message
	channels []string
	pattern  bool
}

func (s *inMemorySubscription) Channel() <-chan Message {
	return s.ch
}

func (s *inMemorySubscription) Unsubscribe() error {
	s.client.pubsubMu.Lock()
	defer s.client.pubsubMu.Unlock()

	if s.pattern {
		for _, pattern := range s.channels {
			subs := s.client.psubscribers[pattern]
			for i, sub := range subs {
				if sub == s.ch {
					s.client.psubscribers[pattern] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
		}
	} else {
		for _, channel := range s.channels {
			subs := s.client.subscribers[channel]
			for i, sub := range subs {
				if sub == s.ch {
					s.client.subscribers[channel] = append(subs[:i], subs[i+1:]...)
					break
				}
			}
		}
	}

	return nil
}
