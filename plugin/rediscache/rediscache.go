// Package rediscache implements plugin.Cache on Redis, using the same key
// layout as the Node.js backend's TagCache (services/redis/TagCache.ts):
// data under "tagcache:data:", tags as "tagcache:tag:" SETs of data-keys.
// Matching this layout (not just tag names) is what lets a write from
// either language invalidate cache entries populated by the other.
package rediscache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	dataPrefix = "tagcache:data:"
	tagPrefix  = "tagcache:tag:"
)

// Cache is a Redis-backed implementation of plugin.Cache, wire-compatible
// with the Node.js TagCache.
type Cache struct {
	client redis.UniversalClient
}

// New wraps an existing Redis client. Any redis.UniversalClient works
// (*redis.Client, *redis.ClusterClient, *redis.Ring), so callers can point
// this at the same Redis deployment (single node, cluster, or sentinel) the
// Node backend already talks to.
func New(client redis.UniversalClient) *Cache {
	return &Cache{client: client}
}

// Get returns the raw bytes stored under key, or (nil, false, nil) on a
// miss — matching plugin.Cache's contract.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool, error) {
	data, err := c.client.Get(ctx, dataPrefix+key).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// Set stores value under key with the given tags and ttl (0 = no expiry,
// applied to the tag SETs too). Tag membership and the value are written
// in one MULTI, and each tag's expiry is refreshed to ttl, so a tag with
// no recent writes just expires instead of holding orphaned members.
func (c *Cache) Set(ctx context.Context, key string, value []byte, tags []string, ttl time.Duration) error {
	pipe := c.client.TxPipeline()
	for _, tag := range tags {
		tagKey := tagPrefix + tag
		pipe.SAdd(ctx, tagKey, key)
		if ttl > 0 {
			pipe.Expire(ctx, tagKey, ttl)
		}
	}
	pipe.Set(ctx, dataPrefix+key, value, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// Invalidate removes every data-key registered under any of tags, then
// removes the tag SETs themselves.
func (c *Cache) Invalidate(ctx context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}

	var dataKeys []string
	for _, tag := range tags {
		keys, err := c.client.SMembers(ctx, tagPrefix+tag).Result()
		if err != nil {
			return err
		}
		dataKeys = append(dataKeys, keys...)
	}

	pipe := c.client.Pipeline()
	for _, key := range dataKeys {
		pipe.Del(ctx, dataPrefix+key)
	}
	for _, tag := range tags {
		pipe.Del(ctx, tagPrefix+tag)
	}
	_, err := pipe.Exec(ctx)
	return err
}
