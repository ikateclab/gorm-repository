package memory

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetGet_RoundTrip(t *testing.T) {
	c := New()
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k", []byte("v"), nil, 0, 0))
	got, ok, _, err := c.Get(ctx, "k", nil)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v"), got)
}

func TestGet_Miss(t *testing.T) {
	c := New()
	got, ok, _, err := c.Get(context.Background(), "missing", nil)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestGet_ReturnsCopy(t *testing.T) {
	c := New()
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k", []byte("orig"), nil, 0, 0))

	got, _, _, _ := c.Get(ctx, "k", nil)
	got[0] = 'X'

	again, _, _, _ := c.Get(ctx, "k", nil)
	assert.Equal(t, []byte("orig"), again, "mutating returned slice must not affect cached value")
}

func TestSet_ReplacesExisting(t *testing.T) {
	c := New()
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k", []byte("v1"), []string{"t1"}, 0, 0))
	require.NoError(t, c.Set(ctx, "k", []byte("v2"), []string{"t2"}, 0, 0))

	got, _, _, _ := c.Get(ctx, "k", nil)
	assert.Equal(t, []byte("v2"), got)
	assert.Equal(t, 1, c.Tags(), "old tag should have been cleaned up")
}

func TestInvalidate_ByTag(t *testing.T) {
	c := New()
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "u1", []byte("v1"), []string{"user:1", "table:users"}, 0, 0))
	require.NoError(t, c.Set(ctx, "u2", []byte("v2"), []string{"user:2", "table:users"}, 0, 0))
	require.NoError(t, c.Set(ctx, "p1", []byte("v3"), []string{"post:1", "table:posts"}, 0, 0))

	require.NoError(t, c.Invalidate(ctx, []string{"table:users"}))

	_, ok1, _, _ := c.Get(ctx, "u1", nil)
	_, ok2, _, _ := c.Get(ctx, "u2", nil)
	_, ok3, _, _ := c.Get(ctx, "p1", nil)
	assert.False(t, ok1)
	assert.False(t, ok2)
	assert.True(t, ok3)
}

func TestInvalidate_MultipleTags(t *testing.T) {
	c := New()
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "a", []byte("a"), []string{"t1"}, 0, 0))
	require.NoError(t, c.Set(ctx, "b", []byte("b"), []string{"t2"}, 0, 0))
	require.NoError(t, c.Set(ctx, "c", []byte("c"), []string{"t3"}, 0, 0))

	require.NoError(t, c.Invalidate(ctx, []string{"t1", "t2"}))

	_, okA, _, _ := c.Get(ctx, "a", nil)
	_, okB, _, _ := c.Get(ctx, "b", nil)
	_, okC, _, _ := c.Get(ctx, "c", nil)
	assert.False(t, okA)
	assert.False(t, okB)
	assert.True(t, okC)
}

func TestTTL_Expiry(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := New(WithClock(func() time.Time { return now }))
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "k", []byte("v"), nil, time.Second, 0))

	// Still valid.
	_, ok, _, _ := c.Get(ctx, "k", nil)
	assert.True(t, ok)

	// Past expiry.
	now = now.Add(2 * time.Second)
	_, ok, _, _ = c.Get(ctx, "k", nil)
	assert.False(t, ok)
	assert.Equal(t, 0, c.Len(), "expired entry should be evicted on read")
}

func TestLRU_Eviction(t *testing.T) {
	c := New(WithMaxEntries(2))
	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "a", []byte("a"), nil, 0, 0))
	require.NoError(t, c.Set(ctx, "b", []byte("b"), nil, 0, 0))

	// Touch a to make it most-recent.
	_, _, _, _ = c.Get(ctx, "a", nil)

	// Inserting c evicts b (least recent).
	require.NoError(t, c.Set(ctx, "c", []byte("c"), nil, 0, 0))

	_, okA, _, _ := c.Get(ctx, "a", nil)
	_, okB, _, _ := c.Get(ctx, "b", nil)
	_, okC, _, _ := c.Get(ctx, "c", nil)
	assert.True(t, okA)
	assert.False(t, okB)
	assert.True(t, okC)
}

func TestUnboundedWhenMaxNonPositive(t *testing.T) {
	c := New(WithMaxEntries(0))
	ctx := context.Background()
	for i := 0; i < 5000; i++ {
		require.NoError(t, c.Set(ctx, key(i), []byte("v"), nil, 0, 0))
	}
	assert.Equal(t, 5000, c.Len())
}

func key(i int) string {
	return "k" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
