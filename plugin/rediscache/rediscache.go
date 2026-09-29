// Package rediscache implements plugin.Cache on Redis, using the same key
// layout as the Node.js backend's TagCache (services/redis/TagCache.ts):
// data under "tagcache:data:", tags as "tagcache:tag:" SETs of data-keys.
// Matching this layout (not just tag names) is what lets a write from
// either language invalidate cache entries populated by the other.
//
// Requires Redis >= 3.2 (EVAL, SPOP with a count).
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

// tagScript adds ARGV[1] to the tag SET in KEYS[1] and keeps the SET alive
// at least as long as its longest-lived member (ARGV[2], in ms; <= 0 means
// no expiry). Blindly re-applying each Set's own ttl would let a
// short-lived entry shorten the SET's life under a longer-lived one; once
// the SET expired, invalidating that tag would no longer reach the
// longer-lived entry. It touches only KEYS[1], so it's Redis Cluster safe.
const tagScript = `
local existed = redis.call('EXISTS', KEYS[1])
redis.call('SADD', KEYS[1], ARGV[1])
local ttl = tonumber(ARGV[2])
if ttl <= 0 then
  redis.call('PERSIST', KEYS[1])
elseif existed == 0 then
  redis.call('PEXPIRE', KEYS[1], ttl)
else
  local cur = redis.call('PTTL', KEYS[1])
  if cur >= 0 and cur < ttl then
    redis.call('PEXPIRE', KEYS[1], ttl)
  end
end
return 1
`

// invalidateBatch caps how many members one SPOP takes from a tag SET.
const invalidateBatch = 1000

// Set stores value under key with the given tags and ttl (0 = no expiry).
// Tag membership and the value are written in one MULTI. Each tag SET's
// expiry is extended to cover ttl but never shortened, and a no-expiry
// entry makes its tag SETs persistent — so a tag with no recent writes
// still expires on its own instead of holding orphaned members forever.
func (c *Cache) Set(ctx context.Context, key string, value []byte, tags []string, ttl time.Duration) error {
	ttlMs := int64(0)
	if ttl > 0 {
		ttlMs = ttl.Milliseconds()
		if ttlMs == 0 {
			ttlMs = 1
		}
	}
	pipe := c.client.TxPipeline()
	for _, tag := range tags {
		pipe.Eval(ctx, tagScript, []string{tagPrefix + tag}, key, ttlMs)
	}
	pipe.Set(ctx, dataPrefix+key, value, ttl)
	_, err := pipe.Exec(ctx)
	return err
}

// Invalidate removes every data-key registered under any of tags. Members
// are SPOPped rather than read with SMEMBERS and the SET deleted
// afterwards: that pair isn't atomic, so a key SADDed in between would be
// dropped from its tag while its data survived, out of reach of any later
// invalidation. Only members present when Invalidate starts are drained;
// keys added concurrently are either popped too or stay tracked.
func (c *Cache) Invalidate(ctx context.Context, tags []string) error {
	for _, tag := range tags {
		tagKey := tagPrefix + tag
		remaining, err := c.client.SCard(ctx, tagKey).Result()
		if err != nil {
			return err
		}
		for remaining > 0 {
			keys, err := c.client.SPopN(ctx, tagKey, invalidateBatch).Result()
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				break
			}
			remaining -= int64(len(keys))

			pipe := c.client.Pipeline()
			for _, key := range keys {
				pipe.Del(ctx, dataPrefix+key)
			}
			if _, err := pipe.Exec(ctx); err != nil {
				// Put the popped keys back so a retry can still reach them.
				members := make([]interface{}, len(keys))
				for i, k := range keys {
					members[i] = k
				}
				_ = c.client.SAdd(ctx, tagKey, members...).Err()
				return err
			}
		}
	}
	return nil
}
