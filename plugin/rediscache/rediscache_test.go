package rediscache

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	redisContainer "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/ikateclab/gorm-repository/plugin"
)

// setupRedis starts a real Redis container so these tests exercise the
// actual wire protocol, not an in-process fake — the whole point of this
// package is compatibility with what the Node.js backend does over the same
// Redis connection.
func setupRedis(t testing.TB) *redis.Client {
	t.Helper()
	ctx := context.Background()

	container, err := redisContainer.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = container.Terminate(context.Background())
	})

	connStr, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	opts, err := redis.ParseURL(connStr)
	require.NoError(t, err)
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Ping(ctx).Err())
	return client
}

func TestRoundTrip_SetThenGet(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()

	_, hit, versions, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.False(t, hit)
	require.NoError(t, c.Set(ctx, "k1", []byte(`{"name":"alice"}`), []string{"Contact:1"}, time.Minute, versions))

	data, hit, _, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.True(t, hit)
	require.JSONEq(t, `{"name":"alice"}`, string(data))
}

// Values are opaque bytes (the plugin stores gob), not JSON.
func TestRoundTrip_BinaryValue(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	value := []byte{'g', 0x00, 0xff, 0x10, '"'}

	require.NoError(t, c.Set(ctx, "k1", value, []string{"Contact:1"}, time.Minute, nil))
	data, hit, _, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, value, data)
}

func TestInvalidate_OnlyTheTaggedEntry(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:1", "Contact:acc-1:list"}, time.Minute, nil))
	require.NoError(t, c.Set(ctx, "k2", []byte(`2`), []string{"Contact:2"}, time.Minute, nil))

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:acc-1:list"}))

	_, hit1, _, _ := c.Get(ctx, "k1", []string{"Contact:1"})
	_, hit2, _, _ := c.Get(ctx, "k2", []string{"Contact:2"})
	require.False(t, hit1)
	require.True(t, hit2)
}

// A read that overlapped a write must not be served, even when the read's
// Set lands after the write's invalidation.
func TestSet_ReadOverlappingAWriteIsNeverServed(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()

	_, _, versions, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	require.NoError(t, c.Set(ctx, "k1", []byte(`"read before the write"`), []string{"Contact:1"}, time.Minute, versions))

	_, hit, _, _ := c.Get(ctx, "k1", []string{"Contact:1"})
	require.False(t, hit)
}

// Tags only the result told aren't passed to Get: the entry's own tags are
// checked on a second round-trip.
func TestGet_ChecksTagsItWasNotGiven(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:acc-1:list"}, time.Minute, nil))

	_, hit, _, _ := c.Get(ctx, "k1", nil)
	require.True(t, hit)
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:acc-1:list"}))
	_, hit, _, _ = c.Get(ctx, "k1", nil)
	require.False(t, hit)
}

func TestInvalidate_AllTagDropsEverything(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:1"}, time.Minute, nil))

	require.NoError(t, c.Invalidate(ctx, []string{"*"}))

	_, hit, _, _ := c.Get(ctx, "k1", []string{"Contact:1"})
	require.False(t, hit)
}

// A version counter that expired and is bumped again must still exceed
// every version recorded before it expired, or older entries would look
// valid again.
func TestBump_MonotonicAcrossCounterExpiry(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	_, _, versions, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:1"}, time.Minute, versions))

	require.NoError(t, client.Del(ctx, verPrefix+"Contact:1").Err()) // the counter expires
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))

	_, hit, _, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.False(t, hit, "a bump after the counter expired must still invalidate older entries")
}

func TestWireLayout_PerTagVersionCounters(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	v1, err := client.Get(ctx, "cache:ver:Contact:1").Int64()
	require.NoError(t, err)
	require.Greater(t, v1, int64(0))
	ttl, err := client.TTL(ctx, "cache:ver:Contact:1").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, DefaultMaxTTL)
	require.LessOrEqual(t, ttl, DefaultMaxTTL+markerSlack)

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	v2, err := client.Get(ctx, "cache:ver:Contact:1").Int64()
	require.NoError(t, err)
	require.Greater(t, v2, v1)

	require.Zero(t, client.Exists(ctx, "cache:seq").Val(), "no global counter")
	require.Zero(t, client.Exists(ctx, "cache:ver:Contact:2").Val(), "untouched tags get no counter")
}

func TestInvalidate_EmptyTagsIsNoop(t *testing.T) {
	client := setupRedis(t)
	require.NoError(t, New(client).Invalidate(context.Background(), nil))
	keys, err := client.Keys(context.Background(), "*").Result()
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestSet_CapsTTLAtMaxTTL(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	for _, tc := range []struct {
		cache *Cache
		max   time.Duration
	}{
		{New(client), DefaultMaxTTL},
		{New(client, WithMaxTTL(30*time.Minute)), 30 * time.Minute},
	} {
		for _, ttl := range []time.Duration{0, time.Hour} {
			require.NoError(t, tc.cache.Set(ctx, "k", []byte(`1`), []string{"Contact:1"}, ttl, nil))
			got, err := client.TTL(ctx, "k").Result()
			require.NoError(t, err)
			require.Greater(t, got, tc.max-time.Minute)
			require.LessOrEqual(t, got, tc.max)
		}
	}
}

func TestWithMaxTTL_StretchesCounterLifetime(t *testing.T) {
	client := setupRedis(t)
	c := New(client, WithMaxTTL(time.Hour))
	ctx := context.Background()

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	ttl, err := client.TTL(ctx, "cache:ver:Contact:1").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Hour, "counters must outlive the longest entry")
}

func TestReadVersions_UnreadableCounterIsAnError(t *testing.T) {
	ok := redis.NewStringCmd(context.Background())
	ok.SetVal("5")
	missing := redis.NewStringCmd(context.Background())
	missing.SetErr(redis.Nil)
	failed := redis.NewStringCmd(context.Background())
	failed.SetErr(errors.New("shard down"))

	v, err := readVersions([]string{"a", "b"}, []*redis.StringCmd{ok, missing})
	require.NoError(t, err)
	require.Equal(t, int64(5), v["a"])
	require.Zero(t, v["b"])

	_, err = readVersions([]string{"a", "c"}, []*redis.StringCmd{ok, failed})
	require.Error(t, err)
}

// pipelineHook counts the pipelines a client sends and their sizes, and
// fails the one numbered failOn (1-based; 0 never fails).
type pipelineHook struct {
	mu     sync.Mutex
	count  int
	sizes  []int
	failOn int
}

var errInjected = errors.New("injected pipeline failure")

func (h *pipelineHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *pipelineHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }

func (h *pipelineHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.mu.Lock()
		h.count++
		h.sizes = append(h.sizes, len(cmds))
		fail := h.failOn != 0 && h.count == h.failOn
		h.mu.Unlock()
		if fail {
			for _, cmd := range cmds {
				cmd.SetErr(errInjected)
			}
			return errInjected
		}
		return next(ctx, cmds)
	}
}

func (h *pipelineHook) pipelines() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// sent returns the size of every pipeline sent so far, in order.
func (h *pipelineHook) sent() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sizes)
}

// failNext makes the n-th pipeline from now fail.
func (h *pipelineHook) failNext(n int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failOn = h.count + n
}

// A read's own tags are checked in the lookup's round trip; tags only the
// entry carries cost exactly one more.
func TestGet_PipelineRoundTrips(t *testing.T) {
	client := setupRedis(t)
	hook := &pipelineHook{}
	client.AddHook(hook)
	c := New(client)
	ctx := context.Background()

	_, hit, versions, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.False(t, hit)
	assert.Equal(t, 1, hook.pipelines(), "a miss is one round trip")

	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:1"}, time.Minute, versions))
	before := hook.pipelines()
	_, hit, _, err = c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.True(t, hit)
	assert.Equal(t, before+1, hook.pipelines(), "a hit on the read's own tags is one round trip")

	require.NoError(t, c.Set(ctx, "k2", []byte(`2`), []string{"Contact:1", "Contact:acc-1:list"}, time.Minute, nil))
	before = hook.pipelines()
	_, hit, _, err = c.Get(ctx, "k2", []string{"Contact:1"})
	require.NoError(t, err)
	require.True(t, hit)
	assert.Equal(t, before+2, hook.pipelines(), "an extra entry tag costs a second round trip")
}

// Without a reliable snapshot nothing can be served or stored: a failed
// round trip, or a counter that can't be read, is an error.
func TestGet_RoundTripErrors(t *testing.T) {
	client := setupRedis(t)
	hook := &pipelineHook{}
	client.AddHook(hook)
	c := New(client)
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k", []byte(`1`), []string{"Contact:1", "Contact:acc-1:list"}, time.Minute, nil))

	hook.failNext(1)
	_, hit, versions, err := c.Get(ctx, "k", []string{"Contact:1"})
	assert.ErrorIs(t, err, errInjected, "first round trip")
	assert.False(t, hit)
	assert.Nil(t, versions)

	hook.failNext(2)
	_, hit, versions, err = c.Get(ctx, "k", []string{"Contact:1"})
	assert.ErrorIs(t, err, errInjected, "second round trip, for the entry's extra tag")
	assert.False(t, hit)
	assert.Nil(t, versions)

	// A counter holding garbage fails the round trip that reads it.
	require.NoError(t, client.Set(ctx, verPrefix+"Contact:acc-1:list", "not-a-number", 0).Err())
	_, hit, _, err = c.Get(ctx, "k", []string{"Contact:1"})
	assert.Error(t, err, "second round trip")
	assert.False(t, hit)
	_, _, _, err = c.Get(ctx, "k", []string{"Contact:acc-1:list"})
	assert.Error(t, err, "first round trip")
}

// manyTags returns n distinct tags.
func manyTags(n int) []string {
	tags := make([]string, n)
	for i := range tags {
		tags[i] = "Contact:" + strconv.Itoa(i)
	}
	return tags
}

// readCounters reads every tag's version counter through client; a missing
// one is left out.
func readCounters(t *testing.T, client redis.UniversalClient, tags []string) plugin.Versions {
	t.Helper()
	ctx := context.Background()
	pipe := client.Pipeline()
	cmds := versionCmds(ctx, pipe, tags)
	if _, err := pipe.Exec(ctx); !errors.Is(err, redis.Nil) {
		require.NoError(t, err)
	}
	versions, err := readVersions(tags, cmds)
	require.NoError(t, err)
	return versions
}

// notBumped lists the tags whose counter didn't move past before.
func notBumped(tags []string, before, after plugin.Versions) []string {
	var out []string
	for _, tag := range tags {
		if after[tag] <= before[tag] {
			out = append(out, tag)
		}
	}
	return out
}

// commandCalls returns how many times Redis ran each command since its
// stats were reset, from INFO commandstats ("evalsha", "script|load").
func commandCalls(t *testing.T, client *redis.Client) map[string]int {
	t.Helper()
	info, err := client.Info(context.Background(), "commandstats").Result()
	require.NoError(t, err)
	calls := map[string]int{}
	for _, line := range strings.Split(info, "\r\n") {
		rest, ok := strings.CutPrefix(line, "cmdstat_")
		if !ok {
			continue
		}
		name, stats, _ := strings.Cut(rest, ":")
		field, _, _ := strings.Cut(stats, ",")
		n, err := strconv.Atoi(strings.TrimPrefix(field, "calls="))
		require.NoError(t, err, line)
		calls[name] = n
	}
	return calls
}

// A write tagging thousands of rows bumps them bumpBatch per pipeline, one
// pipeline after another.
func TestInvalidate_BumpsInBatches(t *testing.T) {
	client := setupRedis(t)
	hook := &pipelineHook{}
	client.AddHook(hook)
	c := New(client)
	tags := manyTags(2500)

	require.NoError(t, c.Invalidate(context.Background(), tags))
	assert.Equal(t, []int{1000, 1000, 500}, hook.sent())
	assert.Empty(t, notBumped(tags, nil, readCounters(t, client, tags)))
}

// The Node backend runs the same script, so the same SHA: changing a byte
// of bumpScript changes it, and breaks the Node side's comparison test.
const bumpScriptSHA = "f982d82a0f288340d86ee49f266a63e2e1c074dc"

// The script is loaded once, then sent by its SHA: Redis gets EVALSHA for
// every tag, never the script's text again.
func TestInvalidate_RunsTheScriptBySHA(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()
	require.Equal(t, bumpScriptSHA, c.bump.Hash(), "bumpScript must stay byte-identical to the Node backend's")
	require.NoError(t, client.ConfigResetStat(ctx).Err())

	require.NoError(t, c.Invalidate(ctx, manyTags(2500)))
	require.NoError(t, c.Invalidate(ctx, manyTags(10)))

	calls := commandCalls(t, client)
	assert.Equal(t, 1, calls["script|load"])
	assert.Equal(t, 2510, calls["evalsha"])
	assert.Zero(t, calls["eval"])
}

// A Redis that lost the script (SCRIPT FLUSH, a restart, a failover)
// answers NOSCRIPT: it's loaded again and the batch resent.
func TestInvalidate_RecoversFromNoScript(t *testing.T) {
	client := setupRedis(t)
	hook := &pipelineHook{}
	client.AddHook(hook)
	c := New(client)
	ctx := context.Background()
	tags := manyTags(2500)

	require.NoError(t, c.Invalidate(ctx, tags))
	before := readCounters(t, client, tags)
	require.NoError(t, client.ScriptFlush(ctx).Err())

	sent := len(hook.sent())
	require.NoError(t, c.Invalidate(ctx, tags))
	assert.Equal(t, []int{1000, 1000, 1000, 500}, hook.sent()[sent:], "the first batch is sent again")
	assert.Empty(t, notBumped(tags, before, readCounters(t, client, tags)))
	assert.Equal(t, []bool{true}, client.ScriptExists(ctx, bumpScriptSHA).Val())
}

// A failed batch fails the call, and the batches after it aren't sent: the
// caller resends every tag, and bumping one again is harmless.
func TestInvalidate_FailedBatchFailsTheCall(t *testing.T) {
	client := setupRedis(t)
	hook := &pipelineHook{}
	client.AddHook(hook)
	c := New(client)
	ctx := context.Background()
	tags := manyTags(2500)

	hook.failNext(2)
	assert.ErrorIs(t, c.Invalidate(ctx, tags), errInjected)
	assert.Equal(t, []int{1000, 1000}, hook.sent(), "the third batch isn't sent")
	versions := readCounters(t, client, tags)
	assert.Empty(t, notBumped(tags[:1000], nil, versions), "the first batch went through")
	assert.Len(t, versions, 1000, "nothing after it did")

	// A bump failing on the server fails its batch too, and isn't taken
	// for a missing script.
	require.NoError(t, client.Set(ctx, verPrefix+tags[1500], "not-a-number", 0).Err())
	sent := len(hook.sent())
	err := c.Invalidate(ctx, tags)
	assert.Error(t, err)
	assert.False(t, redis.HasErrorPrefix(err, "NOSCRIPT"))
	assert.Equal(t, []int{1000, 1000}, hook.sent()[sent:], "the failed batch isn't resent, the next isn't sent")
}

// Ring and Cluster clients spread the counters over several servers: the
// script must be on each, and one that lost it recovers like a single
// Redis. Two standalone servers stand in for the shards, the Cluster
// client getting a fixed slot map.
func TestInvalidate_ShardedClients(t *testing.T) {
	servers := []*redis.Client{setupRedis(t), setupRedis(t)}
	addrA, addrB := servers[0].Options().Addr, servers[1].Options().Addr
	ctx := context.Background()
	tags := manyTags(2500)

	for name, newClient := range map[string]func() redis.UniversalClient{
		"ring": func() redis.UniversalClient {
			return redis.NewRing(&redis.RingOptions{Addrs: map[string]string{"a": addrA, "b": addrB}})
		},
		"cluster": func() redis.UniversalClient {
			return redis.NewClusterClient(&redis.ClusterOptions{
				ClusterSlots: func(context.Context) ([]redis.ClusterSlot, error) {
					return []redis.ClusterSlot{
						{Start: 0, End: 8191, Nodes: []redis.ClusterNode{{Addr: addrA}}},
						{Start: 8192, End: 16383, Nodes: []redis.ClusterNode{{Addr: addrB}}},
					}, nil
				},
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, s := range servers {
				require.NoError(t, s.FlushAll(ctx).Err())
				require.NoError(t, s.ScriptFlush(ctx).Err())
			}
			client := newClient()
			t.Cleanup(func() { _ = client.Close() })
			c := New(client)

			require.NoError(t, c.Invalidate(ctx, tags))
			before := readCounters(t, client, tags)
			assert.Empty(t, notBumped(tags, nil, before))
			for _, s := range servers {
				assert.Positive(t, s.DBSize(ctx).Val(), "counters on every server")
				assert.Equal(t, []bool{true}, s.ScriptExists(ctx, bumpScriptSHA).Val(), "the script on every server")
			}

			for _, s := range servers {
				require.NoError(t, s.ScriptFlush(ctx).Err())
			}
			require.NoError(t, c.Invalidate(ctx, tags))
			assert.Empty(t, notBumped(tags, before, readCounters(t, client, tags)))
		})
	}
}

// An entry that isn't a valid envelope is a miss, not an error: the read
// goes to the database and overwrites it.
func TestGet_CorruptEnvelopeIsAMiss(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))

	valid := string(encodeEntry([]string{"*", "Contact:1"}, nil, []byte("value")))
	for name, raw := range map[string]string{
		"garbage":   "{garbage",
		"empty":     "",
		"truncated": valid[:len(valid)-len("value")-1],
		// What earlier versions stored: an entry from before an upgrade is
		// read again from the database, not misread.
		"json envelope": `{"versions":{"*":0,"Contact:1":0},"tags":["Contact:1"],"value":"dmFsdWU="}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, client.Set(ctx, "k", raw, time.Minute).Err())
			data, hit, versions, err := c.Get(ctx, "k", []string{"Contact:1"})
			require.NoError(t, err)
			assert.False(t, hit)
			assert.Nil(t, data)
			assert.Positive(t, versions["Contact:1"], "versions are still returned for the Set")
		})
	}
}

// An empty value is stored and served like any other, as the memory backend
// does: an entry is only a miss when it's stale or isn't an entry at all.
func TestEmptyValueRoundTrips(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	for _, value := range [][]byte{nil, {}} {
		_, _, versions, err := c.Get(ctx, "k", []string{"Contact:1"})
		require.NoError(t, err)
		require.NoError(t, c.Set(ctx, "k", value, []string{"Contact:1"}, time.Minute, versions))
		data, hit, _, err := c.Get(ctx, "k", []string{"Contact:1"})
		require.NoError(t, err)
		assert.True(t, hit)
		assert.Empty(t, data)
	}
}

func TestEntry_RoundTrip(t *testing.T) {
	for _, value := range [][]byte{nil, {}, []byte("value"), bytes.Repeat([]byte{0, '{', 0xff}, 1000)} {
		raw := encodeEntry([]string{"*", "Contact:1", ""}, plugin.Versions{"*": 3, "Contact:1": 1 << 52}, value)
		e, ok := decodeEntry(raw)
		require.True(t, ok)
		assert.Equal(t, []string{"*", "Contact:1", ""}, e.Tags)
		assert.Equal(t, map[string]int64{"*": 3, "Contact:1": 1 << 52, "": 0}, e.Versions,
			"a tag with no version records 0")
		assert.Equal(t, len(value), len(e.Value))
		assert.True(t, bytes.Equal(value, e.Value))
	}
}

// An entry cut anywhere in its header is rejected, never read as one with
// fewer tags. (The value runs to the end, so a cut there can't be seen.)
func TestDecodeEntry_RejectsTruncatedHeader(t *testing.T) {
	raw := encodeEntry([]string{"*", "Contact:1"}, plugin.Versions{"Contact:1": 300}, []byte("value"))
	header := len(raw) - len("value")
	for i := 0; i < header; i++ {
		_, ok := decodeEntry(raw[:i])
		assert.False(t, ok, "prefix of %d bytes", i)
	}
	_, ok := decodeEntry([]byte{entryFormat, 0xff, 0xff, 0xff, 0xff, 0x0f})
	assert.False(t, ok, "a tag count larger than the entry")
}

func TestWithMaxTTL_NonPositiveIgnored(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Minute} {
		assert.Equal(t, DefaultMaxTTL, New(nil, WithMaxTTL(d)).maxTTL)
	}
}

// A Redis that can't be reached fails every call rather than pretending.
func TestUnreachableRedis_Errors(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = client.Close() })
	c := New(client)
	ctx := context.Background()

	_, hit, _, err := c.Get(ctx, "k", []string{"Contact:1"})
	assert.Error(t, err)
	assert.False(t, hit)
	assert.Error(t, c.Set(ctx, "k", []byte(`1`), []string{"Contact:1"}, time.Minute, nil))
	assert.Error(t, c.Invalidate(ctx, []string{"Contact:1"}))
}
