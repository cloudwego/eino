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

// Package redis provides Redis-based backend implementations for the sseha
// extension points (MetadataStore, EventBus, NodeDiscovery).
//
// These implementations use a minimal RedisClient interface so that any
// Redis client library (go-redis, redigo, etc.) can be adapted.
//
// Usage:
//
//	import (
//	    "github.com/cloudwego/eino/adk/transport/mcp/sseha"
//	    sseharedis "github.com/cloudwego/eino/adk/transport/mcp/sseha/redis"
//	)
//
//	client := adaptYourRedisClient(...)
//	store := sseharedis.NewMetadataStore(&sseharedis.MetadataStoreConfig{Client: client})
//	bus := sseharedis.NewEventBus(&sseharedis.EventBusConfig{Client: client})
//
//	manager, _ := sseha.NewSessionManager(&sseha.SessionManagerConfig{
//	    MetadataStore: store,
//	    EventBus:      bus,
//	    ...
//	})
package redis

import (
	"context"
	"time"
)

// Client is a minimal interface for Redis operations, designed so that
// any Redis client library (go-redis, redigo, etc.) can be adapted to it.
//
// To use with go-redis v9, wrap your *redis.Client with an adapter struct
// that satisfies this interface. Most methods map 1:1 to go-redis commands.
type Client interface {
	// Get retrieves the value for a key. Returns ("", nil) if key does not exist.
	Get(ctx context.Context, key string) (string, error)

	// Set stores a key-value pair with an optional TTL. Zero TTL means no expiry.
	Set(ctx context.Context, key string, value string, ttl time.Duration) error

	// Del deletes one or more keys.
	Del(ctx context.Context, keys ...string) error

	// SetNX sets the value only if the key does not exist (atomic).
	// Returns true if the key was set, false if it already existed.
	SetNX(ctx context.Context, key string, value string, ttl time.Duration) (bool, error)

	// Eval executes a Lua script atomically.
	Eval(ctx context.Context, script string, keys []string, args ...any) (any, error)

	// SMembers returns all members of a set.
	SMembers(ctx context.Context, key string) ([]string, error)

	// SAdd adds members to a set.
	SAdd(ctx context.Context, key string, members ...any) error

	// SRem removes members from a set.
	SRem(ctx context.Context, key string, members ...any) error

	// Publish publishes a message to a Redis Pub/Sub channel.
	Publish(ctx context.Context, channel string, message string) error

	// Subscribe subscribes to Redis Pub/Sub channels and returns a Subscription.
	Subscribe(ctx context.Context, channels ...string) (Subscription, error)

	// PSubscribe subscribes to Redis Pub/Sub channels using pattern matching.
	PSubscribe(ctx context.Context, patterns ...string) (Subscription, error)

	// Close closes the Redis client connection.
	Close() error
}

// Subscription represents an active Redis Pub/Sub subscription.
type Subscription interface {
	// Channel returns a go channel that receives pub/sub messages.
	Channel() <-chan Message

	// Unsubscribe cancels the subscription.
	Unsubscribe() error
}

// Message is a single message received from a Redis Pub/Sub channel.
type Message struct {
	Channel string
	Payload string
}
