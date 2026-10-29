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
// envelope (see encodeEntry) are private to each side.
//
// Requires Redis >= 5 (a script calling TIME before a write).
package rediscache

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ikateclab/gorm-repository/plugin"
)

const (
	verPrefix = "cache:ver:"
	// allTag is on every entry: invalidating it drops the whole cache.
	allTag = "*"
	// DefaultMaxTTL is the default cap on how long an entry lives; raise or
	// lower it with WithMaxTTL.
	DefaultMaxTTL = 15 * time.Minute
	// markerSlack is how much longer than the longest entry a version
	// counter outlives its last bump. It must exceed the time between a
	// read's Get and its Set (the plugin drops Sets older than a minute).
	markerSlack = 2 * time.Minute
	// bumpBatch is how many tags Invalidate bumps per pipeline. A write
	// that tags tens of thousands of rows sends them a batch at a time,
	// not as one pipeline holding every command and reply in memory. The
	// Node backend batches its bumps the same way.
	bumpBatch = 1000
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

// entry is a stored value with the tag versions its read started at.
type entry struct {
	Versions map[string]int64
	Tags     []string
	Value    []byte
}

// entryFormat opens every stored entry. A value that doesn't start with it
// (e.g. the JSON envelope earlier versions stored) reads as a miss.
const entryFormat byte = 1

// encodeEntry renders an entry as its format byte, the number of tags,
// each tag (length-prefixed) with its version, then the value as is: no
// escaping or base64 for Redis to store and the next read to undo.
func encodeEntry(tags []string, versions plugin.Versions, value []byte) []byte {
	size := 1 + binary.MaxVarintLen64 + len(value)
	for _, tag := range tags {
		size += 2*binary.MaxVarintLen64 + len(tag)
	}
	b := make([]byte, 0, size)
	b = append(b, entryFormat)
	b = binary.AppendUvarint(b, uint64(len(tags)))
	for _, tag := range tags {
		b = binary.AppendUvarint(b, uint64(len(tag)))
		b = append(b, tag...)
		// A tag Get wasn't asked about records 0, so any bump at all
		// makes the entry stale: safe, at worst a miss.
		b = binary.AppendVarint(b, versions[tag])
	}
	return append(b, value...)
}

// decodeEntry parses what encodeEntry rendered; ok is false for anything
// else. The value aliases raw.
func decodeEntry(raw []byte) (e entry, ok bool) {
	if len(raw) == 0 || raw[0] != entryFormat {
		return entry{}, false
	}
	raw = raw[1:]
	n, size := binary.Uvarint(raw)
	// Each tag takes at least two bytes. A corrupt count past that is
	// rejected, and one within it still only sizes a few tags up front.
	if size <= 0 || n > uint64(len(raw)/2) {
		return entry{}, false
	}
	raw = raw[size:]
	hint := int(min(n, 16))
	e.Tags = make([]string, 0, hint)
	e.Versions = make(map[string]int64, hint)
	for i := uint64(0); i < n; i++ {
		l, size := binary.Uvarint(raw)
		if size <= 0 || l > uint64(len(raw)-size) {
			return entry{}, false
		}
		tag := string(raw[size : size+int(l)])
		raw = raw[size+int(l):]
		v, size := binary.Varint(raw)
		if size <= 0 {
			return entry{}, false
		}
		raw = raw[size:]
		e.Tags = append(e.Tags, tag)
		e.Versions[tag] = v
	}
	e.Value = raw
	return e, true
}

// Option configures a Cache.
type Option func(*Cache)

// WithMaxTTL sets how long an entry may live, replacing the 15-minute
// DefaultMaxTTL. Every entry is clamped to it, a requested TTL of 0 ("no
// expiry") included: version counters only outlive the longest entry by
// markerSlack, and an entry outliving its tags' counters could be served
// as valid after they expire. A longer cap keeps counters around longer.
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
	// bump runs bumpScript by its SHA; loaded is set once it was loaded.
	bump   *redis.Script
	loaded atomic.Bool
}

var _ plugin.Cache = (*Cache)(nil)

// New wraps an existing Redis client. Any redis.UniversalClient works
// (*redis.Client, *redis.ClusterClient, *redis.Ring), so callers can point
// this at the same Redis deployment the Node backend already talks to.
func New(client redis.UniversalClient, opts ...Option) *Cache {
	c := &Cache{client: client, maxTTL: DefaultMaxTTL, bump: redis.NewScript(bumpScript)}
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

	raw, err := data.Bytes()
	if err != nil {
		return nil, false, versions, nil
	}
	e, ok := decodeEntry(raw)
	if !ok {
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
	return c.client.Set(ctx, key, encodeEntry(withAllTag(tags), versions, value), ttl).Err()
}

// Invalidate bumps every tag's version, bumpBatch tags per pipeline, one
// pipeline after another, running bumpScript by its SHA. Entries aren't
// deleted: the next read of each finds it stale.
//
// A failed batch fails the call with its error, though the batches before
// it went through: bumping a tag again is harmless, so the caller can
// retry every tag.
func (c *Cache) Invalidate(ctx context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	if !c.loaded.Load() {
		if err := c.loadBump(ctx); err != nil {
			return err
		}
	}
	ttlMs := strconv.FormatInt(c.markerTTL().Milliseconds(), 10)
	for batch := range slices.Chunk(tags, bumpBatch) {
		noScript, err := c.bumpTags(ctx, batch, ttlMs)
		if noScript {
			// A node lost the script (SCRIPT FLUSH, a restart) or never
			// had it (a failover, a new shard): load it, resend the batch.
			if err := c.loadBump(ctx); err != nil {
				return err
			}
			_, err = c.bumpTags(ctx, batch, ttlMs)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// bumpTags bumps one batch of tags in a single pipeline. noScript reports
// whether any node lacked the script: in a cluster only some may, so
// theirs isn't necessarily the error Exec returns.
func (c *Cache) bumpTags(ctx context.Context, tags []string, ttlMs string) (noScript bool, err error) {
	pipe := c.client.Pipeline()
	for _, tag := range tags {
		c.bump.EvalSha(ctx, pipe, []string{verPrefix + tag}, ttlMs)
	}
	cmds, err := pipe.Exec(ctx)
	if err == nil {
		return false, nil
	}
	for _, cmd := range cmds {
		if redis.HasErrorPrefix(cmd.Err(), "NOSCRIPT") {
			return true, err
		}
	}
	return false, err
}

// loadBump loads bumpScript on every node a bump can be sent to. A plain
// SCRIPT LOAD doesn't, through a ClusterClient (it also goes to replicas,
// failing while one is down, though bumps only go to masters) or a Ring
// (it goes to one random shard).
func (c *Cache) loadBump(ctx context.Context) error {
	load := func(ctx context.Context, node *redis.Client) error {
		return c.bump.Load(ctx, node).Err()
	}
	var err error
	switch client := c.client.(type) {
	case *redis.ClusterClient:
		err = client.ForEachMaster(ctx, load)
	case *redis.Ring:
		err = client.ForEachShard(ctx, load)
	default:
		err = c.bump.Load(ctx, client).Err()
	}
	if err == nil {
		c.loaded.Store(true)
	}
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
