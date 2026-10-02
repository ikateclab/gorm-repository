package rediscache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	redisContainer "github.com/testcontainers/testcontainers-go/modules/redis"
)

// setupRedis starts a real Redis container so these tests exercise the
// actual wire protocol, not an in-process fake — the whole point of this
// package is compatibility with what the Node.js backend does over the same
// Redis connection.
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
	c := New(setupRedis(t))
	ctx := context.Background()

	_, hit, seq, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.False(t, hit)
	require.NoError(t, c.Set(ctx, "k1", []byte(`{"name":"alice"}`), []string{"Contact:1"}, time.Minute, seq))

	data, hit, _, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.True(t, hit)
	require.JSONEq(t, `{"name":"alice"}`, string(data))
}

func TestInvalidate_OnlyTheTaggedEntry(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:1", "Contact:acc-1:list"}, time.Minute, 0))
	require.NoError(t, c.Set(ctx, "k2", []byte(`2`), []string{"Contact:2"}, time.Minute, 0))

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

	_, _, seq, err := c.Get(ctx, "k1", []string{"Contact:1"})
	require.NoError(t, err)
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	require.NoError(t, c.Set(ctx, "k1", []byte(`"read before the write"`), []string{"Contact:1"}, time.Minute, seq))

	_, hit, _, _ := c.Get(ctx, "k1", []string{"Contact:1"})
	require.False(t, hit)
}

// Tags only the result told aren't passed to Get: the entry's own tags are
// checked on a second round-trip.
func TestGet_ChecksTagsItWasNotGiven(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:acc-1:list"}, time.Minute, 0))

	_, hit, _, _ := c.Get(ctx, "k1", nil)
	require.True(t, hit)
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:acc-1:list"}))
	_, hit, _, _ = c.Get(ctx, "k1", nil)
	require.False(t, hit)
}

func TestInvalidate_AllTagDropsEverything(t *testing.T) {
	c := New(setupRedis(t))
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k1", []byte(`1`), []string{"Contact:1"}, time.Minute, 0))

	require.NoError(t, c.Invalidate(ctx, []string{"*"}))

	_, hit, _, _ := c.Get(ctx, "k1", []string{"Contact:1"})
	require.False(t, hit)
}

func TestInvalidate_OutOfOrderKeepsTheNewest(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))

	// An older invalidation of the same tag landing last.
	require.NoError(t, client.ZAddGT(ctx, invPrefix+"Contact:1", redis.Z{Score: 1, Member: invMember}).Err())

	score, err := client.ZScore(ctx, invPrefix+"Contact:1", invMember).Result()
	require.NoError(t, err)
	require.Equal(t, float64(2), score)
}

func TestWireLayout_MatchesNodeBackend(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "mykey", []byte(`"hello"`), []string{"Contact:1"}, time.Minute, 7))
	raw, err := client.Get(ctx, "mykey").Result()
	require.NoError(t, err)
	require.JSONEq(t, `{"seq":7,"tags":["Contact:1"],"value":"hello"}`, raw)

	require.NoError(t, c.Invalidate(ctx, []string{"Contact:1"}))
	seq, err := client.Get(ctx, "cache:seq").Int64()
	require.NoError(t, err)
	score, err := client.ZScore(ctx, "cache:inv:Contact:1", "seq").Result()
	require.NoError(t, err)
	require.Equal(t, float64(seq), score)
	ttl, err := client.TTL(ctx, "cache:inv:Contact:1").Result()
	require.NoError(t, err)
	require.Greater(t, ttl, time.Duration(0))
}

func TestInvalidate_EmptyTagsIsNoop(t *testing.T) {
	client := setupRedis(t)
	require.NoError(t, New(client).Invalidate(context.Background(), nil))
	require.Zero(t, client.Exists(context.Background(), "cache:seq").Val())
}

func TestSet_CapsTTLBelowTheMarkerLifetime(t *testing.T) {
	client := setupRedis(t)
	c := New(client)
	ctx := context.Background()

	for _, ttl := range []time.Duration{0, time.Hour} {
		require.NoError(t, c.Set(ctx, "k", []byte(`1`), []string{"Contact:1"}, ttl, 0))
		got, err := client.TTL(ctx, "k").Result()
		require.NoError(t, err)
		require.Greater(t, got, time.Duration(0))
		require.LessOrEqual(t, got, maxTTL)
	}
}

func TestInvalidatedAfter_UnreadableMarkerCountsAsInvalidated(t *testing.T) {
	ok := redis.NewFloatCmd(context.Background())
	failed := redis.NewFloatCmd(context.Background())
	failed.SetErr(errors.New("shard down"))
	missing := redis.NewFloatCmd(context.Background())
	missing.SetErr(redis.Nil)

	require.False(t, invalidatedAfter([]*redis.FloatCmd{ok, missing}, 1))
	require.True(t, invalidatedAfter([]*redis.FloatCmd{ok, failed}, 1))
}
