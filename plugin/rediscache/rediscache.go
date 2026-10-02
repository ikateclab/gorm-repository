// Package rediscache implements plugin.Cache on Redis, with the same layout
// as the Node.js backend's TagCache (services/redis/TagCache.ts) — matching
// the layout, not just the tag names, is what lets a write from either
// language invalidate what the other cached.
//
// Every invalidation takes the next "cache:seq" and records it on each tag
// ("cache:inv:{tag}"); an entry keeps the seq its read started at, and is
// served only while none of its tags was invalidated after that.
package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ikateclab/gorm-repository/plugin"
	"github.com/redis/go-redis/v9"
)

const (
	seqKey    = "cache:seq"
	invPrefix = "cache:inv:"
	invMember = "seq"
	// allTag is on every entry: invalidating it drops the whole cache.
	allTag = "*"
	// invTTL must outlive every entry that can depend on an invalidation:
	// Set caps entries at maxTTL, and is expected within invTTL-maxTTL of
	// its Get.
	invTTL = 15 * time.Minute
	maxTTL = 13 * time.Minute
)

type entry struct {
	Seq   int64           `json:"seq"`
	Tags  []string        `json:"tags"`
	Value json.RawMessage `json:"value"`
}

// Cache is a Redis-backed implementation of plugin.Cache, wire-compatible
// with the Node.js TagCache.
type Cache struct {
	client redis.UniversalClient
}

var _ plugin.Cache = (*Cache)(nil)

// New wraps an existing Redis client. Any redis.UniversalClient works
// (*redis.Client, *redis.ClusterClient, *redis.Ring), so callers can point
// this at the same Redis deployment the Node backend already talks to.
func New(client redis.UniversalClient) *Cache {
	return &Cache{client: client}
}

// Get checks tags in the same round-trip as the lookup; any other tag the
// entry carries costs a second one.
func (c *Cache) Get(ctx context.Context, key string, tags []string) ([]byte, bool, int64, error) {
	known := append([]string{allTag}, tags...)
	pipe := c.client.Pipeline()
	data := pipe.Get(ctx, key)
	seqCmd := pipe.Get(ctx, seqKey)
	invs := invalidations(ctx, pipe, known)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, false, 0, err
	}
	seq, _ := seqCmd.Int64()

	var e entry
	raw, err := data.Bytes()
	if err != nil || json.Unmarshal(raw, &e) != nil || e.Value == nil || invalidatedAfter(invs, e.Seq) {
		return nil, false, seq, nil
	}
	var extra []string
	for _, tag := range e.Tags {
		if !slices.Contains(known, tag) {
			extra = append(extra, tag)
		}
	}
	if len(extra) > 0 {
		pipe := c.client.Pipeline()
		invs := invalidations(ctx, pipe, extra)
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return nil, false, 0, err
		}
		if invalidatedAfter(invs, e.Seq) {
			return nil, false, seq, nil
		}
	}
	return e.Value, true, seq, nil
}

// Set stores value with its tags and the seq its read started at. The ttl
// is capped at maxTTL (0 included): an entry that outlived the marker of an
// invalidation would be served as valid again.
func (c *Cache) Set(ctx context.Context, key string, value []byte, tags []string, ttl time.Duration, seq int64) error {
	if ttl <= 0 || ttl > maxTTL {
		ttl = maxTTL
	}
	raw, err := json.Marshal(entry{Seq: seq, Tags: tags, Value: value})
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, raw, ttl).Err()
}

// Invalidate records the next seq on every tag. Entries aren't deleted:
// the next read of each finds it stale.
func (c *Cache) Invalidate(ctx context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	seq, err := c.client.Incr(ctx, seqKey).Result()
	if err != nil {
		return err
	}
	pipe := c.client.Pipeline()
	for _, tag := range tags {
		// GT keeps the newest seq when invalidations land out of order.
		pipe.ZAddGT(ctx, invPrefix+tag, redis.Z{Score: float64(seq), Member: invMember})
		pipe.Expire(ctx, invPrefix+tag, invTTL)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func invalidations(ctx context.Context, pipe redis.Pipeliner, tags []string) []*redis.FloatCmd {
	cmds := make([]*redis.FloatCmd, len(tags))
	for i, tag := range tags {
		cmds[i] = pipe.ZScore(ctx, invPrefix+tag, invMember)
	}
	return cmds
}

// invalidatedAfter also counts a marker it couldn't read (e.g. one shard
// down) as an invalidation: better a miss than a stale hit.
func invalidatedAfter(cmds []*redis.FloatCmd, seq int64) bool {
	for _, cmd := range cmds {
		if err := cmd.Err(); err != nil && !errors.Is(err, redis.Nil) {
			return true
		}
		if cmd.Val() > float64(seq) {
			return true
		}
	}
	return false
}
