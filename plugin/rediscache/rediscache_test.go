package rediscache

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	redisContainer "github.com/testcontainers/testcontainers-go/modules/redis"
)

// setupRedis starts a real Redis container so these tests exercise the
// actual wire protocol (SADD/SET/MULTI/SMEMBERS/DEL), not an in-process
// fake — the whole point of this package is compatibility with what the
// Node.js backend does over the same Redis connection.
func setupRedis(t *testing.T) *redis.Client {
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
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte(`{"name":"alice"}`), []string{"Contact:1"}, time.Minute))

	data, hit, err := c.Get(ctx, "k1")
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, `{"name":"alice"}`, string(data))
}

func TestGet_Miss(t *testing.T) {
	client := setupRedis(t)
	c := New(client)

	data, hit, err := c.Get(context.Background(), "does-not-exist")
	require.NoError(t, err)
	require.False(t, hit)
	require.Nil(t, data)
}

func TestInvalidate_RemovesTaggedEntry(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), []string{"Contact:1", "Contact:acc-1:list"}, time.Minute))
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))

	_, hit, err := c.Get(ctx, "k1")
	require.NoError(t, err)
	require.False(t, hit, "invalidating one of the entry's tags must evict it")
}

func TestInvalidate_UnrelatedEntrySurvives(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), []string{"Contact:1"}, time.Minute))
	require.NoError(t, c.Set(ctx, "k2", []byte("v2"), []string{"Contact:2"}, time.Minute))

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))

	_, hit1, _ := c.Get(ctx, "k1")
	require.False(t, hit1)
	data2, hit2, err := c.Get(ctx, "k2")
	require.NoError(t, err)
	require.True(t, hit2)
	require.Equal(t, "v2", string(data2))
}

func TestInvalidate_MultipleEntriesUnderSameTag(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	// Two different cached queries both tagged with the same account-list
	// tag — a single write to that account must evict both, the same way
	// a write busts every list query cached under the Node backend's
	// "{Resource}:{accountId}:list" tag.
	require.NoError(t, c.Set(ctx, "list-a", []byte("[1,2]"), []string{"Contact:acc-1:list"}, time.Minute))
	require.NoError(t, c.Set(ctx, "list-b", []byte("[3]"), []string{"Contact:acc-1:list", "Contact:no-account:list"}, time.Minute))

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:acc-1:list"}))

	_, hitA, _ := c.Get(ctx, "list-a")
	_, hitB, _ := c.Get(ctx, "list-b")
	require.False(t, hitA)
	require.False(t, hitB)
}

func TestInvalidate_TagSetItselfIsCleared(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), []string{"Contact:1"}, time.Minute))
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))

	exists, err := client.Exists(ctx, tagPrefix+"Contact:1").Result()
	require.NoError(t, err)
	require.Zero(t, exists, "the tag SET itself must be removed, not just the data it pointed to")
}

func TestWireLayout_MatchesNodeBackend(t *testing.T) {
	// Regression guard for cross-language compatibility: a write from the
	// Node backend (or a hand-rolled script standing in for it) manipulates
	// these exact Redis keys directly — SADD on "tagcache:tag:<tag>", SET
	// on "tagcache:data:<key>" — so Go's Cache must read/write the same
	// physical layout, not just tags that happen to look similar.
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "mykey", []byte("hello"), []string{"Contact:1"}, time.Minute))

	raw, err := client.Get(ctx, "tagcache:data:mykey").Result()
	require.NoError(t, err)
	require.Equal(t, "hello", raw)

	members, err := client.SMembers(ctx, "tagcache:tag:Contact:1").Result()
	require.NoError(t, err)
	require.Equal(t, []string{"mykey"}, members)

	// Simulate a Node-side invalidation touching the same keys directly.
	require.NoError(t, client.Del(ctx, "tagcache:data:mykey").Err())
	_, hit, err := c.Get(ctx, "mykey")
	require.NoError(t, err)
	require.False(t, hit)
}

func TestSet_NoTTL(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), nil, 0))

	ttl, err := client.TTL(ctx, dataPrefix+"k1").Result()
	require.NoError(t, err)
	require.Equal(t, time.Duration(-1), ttl, "ttl<=0 should mean no expiry, matching a falsy timeout in the Node backend")
}

func TestInvalidate_EmptyTagsIsNoop(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	require.NoError(t, c.Invalidate(context.Background(), nil))
}

// TestSet_TagSetGetsExpiry guards against unbounded growth: without an
// expiry of its own, a tag SET keeps every key ever added to it, including
// ones whose own data-key has long since expired via TTL — for a tag that
// rarely gets invalidated, that set only ever grows. Refreshing the tag
// SET's expiry to the same ttl on every Set means a tag nothing touches
// for a while disappears on its own instead of accumulating forever.
func TestSet_TagSetGetsExpiry(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), []string{"Contact:1"}, time.Minute))

	ttl, err := client.TTL(ctx, tagPrefix+"Contact:1").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0), "the tag SET itself must expire, not just the data-key")
	require.LessOrEqual(t, ttl, time.Minute)
}

// TestSet_NoTTL_TagSetAlsoNeverExpires is TestSet_NoTTL's counterpart for
// tag SETs: a ttl<=0 data-key (no expiry) must not leave its tag SETs
// expiring underneath it either.
func TestSet_NoTTL_TagSetAlsoNeverExpires(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), []string{"Contact:1"}, 0))

	ttl, err := client.TTL(ctx, tagPrefix+"Contact:1").Result()
	require.NoError(t, err)
	require.Equal(t, time.Duration(-1), ttl)
}

// TestSet_RefreshesExistingTagTTL confirms a later Set touching an
// already-expiring tag pushes its expiry back out, so a tag that keeps
// getting written to (even under a short-lived entry) doesn't expire out
// from under still-live members added earlier under a longer one.
func TestSet_RefreshesExistingTagTTL(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k1", []byte("v1"), []string{"Contact:1"}, time.Second))
	require.NoError(t, c.Set(ctx, "k2", []byte("v2"), []string{"Contact:1"}, time.Hour))

	ttl, err := client.TTL(ctx, tagPrefix+"Contact:1").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Minute, "the second Set's longer ttl must have refreshed the tag's expiry")
}
