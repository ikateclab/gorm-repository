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

	require.NoError(t, c.Set(ctx, "k", []byte("v"), nil, 0, nil))
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
	require.NoError(t, c.Set(ctx, "k", []byte("orig"), nil, 0, nil))

	got, _, _, _ := c.Get(ctx, "k", nil)
	got[0] = 'X'

	again, _, _, _ := c.Get(ctx, "k", nil)
	assert.Equal(t, []byte("orig"), again, "mutating returned slice must not affect cached value")
}

func TestSet_ReplacesExisting(t *testing.T) {
	c := New()
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "k", []byte("v1"), []string{"t1"}, 0, nil))
	require.NoError(t, c.Set(ctx, "k", []byte("v2"), []string{"t2"}, 0, nil))

	got, _, _, _ := c.Get(ctx, "k", nil)
	assert.Equal(t, []byte("v2"), got)
	assert.Equal(t, 1, c.Tags(), "old tag should have been cleaned up")
}

func TestInvalidate_ByTag(t *testing.T) {
	c := New()
	ctx := context.Background()
	require.NoError(t, c.Set(ctx, "u1", []byte("v1"), []string{"user:1", "table:users"}, 0, nil))
	require.NoError(t, c.Set(ctx, "u2", []byte("v2"), []string{"user:2", "table:users"}, 0, nil))
	require.NoError(t, c.Set(ctx, "p1", []byte("v3"), []string{"post:1", "table:posts"}, 0, nil))

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
	require.NoError(t, c.Set(ctx, "a", []byte("a"), []string{"t1"}, 0, nil))
	require.NoError(t, c.Set(ctx, "b", []byte("b"), []string{"t2"}, 0, nil))
	require.NoError(t, c.Set(ctx, "c", []byte("c"), []string{"t3"}, 0, nil))

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

	require.NoError(t, c.Set(ctx, "k", []byte("v"), nil, time.Second, nil))

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

	require.NoError(t, c.Set(ctx, "a", []byte("a"), nil, 0, nil))
	require.NoError(t, c.Set(ctx, "b", []byte("b"), nil, 0, nil))

	// Touch a to make it most-recent.
	_, _, _, _ = c.Get(ctx, "a", nil)

	// Inserting c evicts b (least recent).
	require.NoError(t, c.Set(ctx, "c", []byte("c"), nil, 0, nil))

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
		require.NoError(t, c.Set(ctx, key(i), []byte("v"), nil, 0, nil))
	}
	assert.Equal(t, 5000, c.Len())
}

// A read that overlapped an invalidation of one of its tags must not be
// stored: its value may predate the write.
func TestSet_RefusedAfterOverlappingInvalidation(t *testing.T) {
	c := New()
	ctx := context.Background()

	_, _, versions, err := c.Get(ctx, "k", []string{"t1"})
	require.NoError(t, err)
	require.NoError(t, c.Invalidate(ctx, []string{"t1"}))
	require.NoError(t, c.Set(ctx, "k", []byte("old"), []string{"t1"}, 0, versions))

	_, ok, _, _ := c.Get(ctx, "k", nil)
	assert.False(t, ok)

	// A read that started after the invalidation stores normally.
	_, _, versions, _ = c.Get(ctx, "k", []string{"t1"})
	require.NoError(t, c.Set(ctx, "k", []byte("new"), []string{"t1"}, 0, versions))
	_, ok, _, _ = c.Get(ctx, "k", nil)
	assert.True(t, ok)
}

// The invalidation map stays bounded; a read older than what was pruned
// is refused rather than risk a stale store.
func TestInvalidations_PrunedConservatively(t *testing.T) {
	c := New(WithMaxTrackedTags(10))
	ctx := context.Background()

	_, _, old, _ := c.Get(ctx, "k", []string{"tag-0"})
	for i := 0; i < 50; i++ {
		require.NoError(t, c.Invalidate(ctx, []string{"tag-" + itoa(i)}))
	}
	assert.LessOrEqual(t, len(c.inv), 10)

	require.NoError(t, c.Set(ctx, "k", []byte("old"), []string{"tag-0"}, 0, old))
	_, ok, _, _ := c.Get(ctx, "k", nil)
	assert.False(t, ok, "tag-0's invalidation was pruned; the older read must still be refused")

	_, _, fresh, _ := c.Get(ctx, "k", []string{"tag-0"})
	require.NoError(t, c.Set(ctx, "k", []byte("new"), []string{"tag-0"}, 0, fresh))
	_, ok, _, _ = c.Get(ctx, "k", nil)
	assert.True(t, ok)
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
