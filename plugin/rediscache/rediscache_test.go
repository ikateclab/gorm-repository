package rediscache

import (
	"bytes"
	"context"
	"errors"
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

// pipelineHook counts the pipelines a client sends, and fails the one
// numbered failOn (1-based; 0 never fails).
type pipelineHook struct {
	mu     sync.Mutex
	count  int
	failOn int
}

var errInjected = errors.New("injected pipeline failure")

func (h *pipelineHook) DialHook(next redis.DialHook) redis.DialHook          { return next }
func (h *pipelineHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook { return next }

func (h *pipelineHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		h.mu.Lock()
		h.count++
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
