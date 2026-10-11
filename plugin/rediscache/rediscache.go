// Package rediscache implements plugin.Cache on Redis.
//
// Every tag has its own version counter ("cache:ver:{tag}"); invalidating
// a tag bumps it. An entry records the versions of its tags as of the
// moment its read started, and is served only while none of them has been
// bumped since. Versions are per tag, so no single key is touched by every
// read and invalidation (which would pin all cache traffic to one shard).
//
// A counter expires markerTTL after its last bump. When it's recreated it
// must still exceed every version an entry recorded earlier, so a bump
// sets it to max(current+1, the Redis server's clock in µs) — monotonic
// across expiry as long as server clocks are roughly in sync.
//
// The "cache:ver:" counters are the part shared with the Node.js backend:
// both sides must bump them the same way (see bumpScript) for a write from
// either language to invalidate what the other cached. Data keys and their
// envelope are private to each side.
//
// Requires Redis >= 5 (EVAL calling TIME before a write).
package rediscache

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ikateclab/gorm-repository/plugin"
)

const (
	verPrefix = "cache:ver:"
	// allTag is on every entry: invalidating it drops the whole cache.
	allTag = "*"
	// DefaultMaxTTL caps entries unless WithMaxTTL says otherwise.
	DefaultMaxTTL = 13 * time.Minute
	// markerSlack is how much longer than the longest entry a version
	// counter outlives its last bump. It must exceed the time between a
	// read's Get and its Set (the plugin drops Sets older than a minute).
	markerSlack = 2 * time.Minute
)

// bumpScript advances one tag's version: max(current+1, server time in
// µs), stored with a PX expiry of ARGV[1]. It touches only KEYS[1], so it
// is Redis Cluster safe. Lua numbers are doubles, exact for µs timestamps.
const bumpScript = `
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local nxt = string.format('%.0f', math.max(cur + 1, now))
redis.call('SET', KEYS[1], nxt, 'PX', ARGV[1])
return nxt
`

type entry struct {
	Versions map[string]int64 `json:"versions"`
	Tags     []string         `json:"tags"`
	Value    []byte           `json:"value"`
}

// Option configures a Cache.
type Option func(*Cache)

// WithMaxTTL caps how long an entry lives (default DefaultMaxTTL). Every
// entry is clamped to it, a requested TTL of 0 ("no expiry") included,
// because version counters only outlive entries by markerSlack; a longer
// cap keeps counters around longer too.
func WithMaxTTL(d time.Duration) Option {
	return func(c *Cache) {
		if d > 0 {
			c.maxTTL = d
		}
	}
}

// Cache is a Redis-backed implementation of plugin.Cache.
type Cache struct {
	client redis.UniversalClient
	maxTTL time.Duration
}

var _ plugin.Cache = (*Cache)(nil)

// New wraps an existing Redis client. Any redis.UniversalClient works
// (*redis.Client, *redis.ClusterClient, *redis.Ring), so callers can point
// this at the same Redis deployment the Node backend already talks to.
func New(client redis.UniversalClient, opts ...Option) *Cache {
	c := &Cache{client: client, maxTTL: DefaultMaxTTL}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Cache) markerTTL() time.Duration { return c.maxTTL + markerSlack }

// Get returns key's value if none of its tags was invalidated since it was
// read, plus the current versions of tags (for a Set after a miss). The
// read's own tags are checked in the same round-trip as the lookup; any
// other tag the entry carries costs a second one.
func (c *Cache) Get(ctx context.Context, key string, tags []string) ([]byte, bool, plugin.Versions, error) {
	known := withAllTag(tags)
	pipe := c.client.Pipeline()
	data := pipe.Get(ctx, key)
	verCmds := versionCmds(ctx, pipe, known)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, false, nil, err
	}
	versions, err := readVersions(known, verCmds)
	if err != nil {
		// Without a reliable snapshot nothing can be served or stored.
		return nil, false, nil, err
	}

	var e entry
	raw, err := data.Bytes()
	if err != nil || json.Unmarshal(raw, &e) != nil || e.Value == nil {
		return nil, false, versions, nil
	}
	if bumpedSince(e, versions) {
		return nil, false, versions, nil
	}

	var extra []string
	for _, tag := range e.Tags {
		if !slices.Contains(known, tag) {
			extra = append(extra, tag)
		}
	}
	if len(extra) > 0 {
		pipe := c.client.Pipeline()
		cmds := versionCmds(ctx, pipe, extra)
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return nil, false, nil, err
		}
		current, err := readVersions(extra, cmds)
		if err != nil {
			return nil, false, nil, err
		}
		if bumpedSince(e, current) {
			return nil, false, versions, nil
		}
	}
	return e.Value, true, versions, nil
}

// Set stores value with its tags and the versions its read started at. The
// ttl is clamped to the max TTL (0 included): an entry that outlived its
// tags' version counters could be served as valid again.
func (c *Cache) Set(ctx context.Context, key string, value []byte, tags []string, ttl time.Duration, versions plugin.Versions) error {
	if ttl <= 0 || ttl > c.maxTTL {
		ttl = c.maxTTL
	}
	all := withAllTag(tags)
	recorded := make(map[string]int64, len(all))
	for _, tag := range all {
		// A tag Get wasn't asked about records 0, so any bump at all
		// makes the entry stale: safe, at worst a miss.
		recorded[tag] = versions[tag]
	}
	raw, err := json.Marshal(entry{Versions: recorded, Tags: tags, Value: value})
	if err != nil {
		return err
	}
	return c.client.Set(ctx, key, raw, ttl).Err()
}

// Invalidate bumps every tag's version. Entries aren't deleted: the next
// read of each finds it stale.
func (c *Cache) Invalidate(ctx context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	ttlMs := strconv.FormatInt(c.markerTTL().Milliseconds(), 10)
	pipe := c.client.Pipeline()
	for _, tag := range tags {
		pipe.Eval(ctx, bumpScript, []string{verPrefix + tag}, ttlMs)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func withAllTag(tags []string) []string {
	out := []string{allTag}
	for _, t := range tags {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func versionCmds(ctx context.Context, pipe redis.Pipeliner, tags []string) []*redis.StringCmd {
	cmds := make([]*redis.StringCmd, len(tags))
	for i, tag := range tags {
		cmds[i] = pipe.Get(ctx, verPrefix+tag)
	}
	return cmds
}

// readVersions maps tags to their current versions; a missing counter is
// 0. A counter it couldn't read (e.g. one shard down) is an error: better a
// miss than a stale hit.
func readVersions(tags []string, cmds []*redis.StringCmd) (plugin.Versions, error) {
	out := make(plugin.Versions, len(tags))
	for i, cmd := range cmds {
		v, err := cmd.Int64()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[tags[i]] = v
	}
	return out, nil
}

// bumpedSince reports whether any tag in current moved past the version e
// recorded for it.
func bumpedSince(e entry, current plugin.Versions) bool {
	for tag, v := range current {
		if v > e.Versions[tag] {
			return true
		}
	}
	return false
}
