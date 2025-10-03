package cache

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// CachedData represents any cached data
type CachedData interface{}

// RawKey represents a raw cache key (can be string or any serializable type)
type RawKey interface{}

// RawTag represents a raw cache tag (can be string or any serializable type)
type RawTag interface{}

// Value represents the structure stored in Redis (matching Node.js structure)
type Value struct {
	RawKey interface{} `json:"rawKey"`
	Tags   []string    `json:"tags,omitempty"`
	Value  interface{} `json:"value"`
}

// Config interface for configuration
type Config interface {
	Get(key string) interface{}
}

// Logger interface for logging
type Logger interface {
	Log(message string)
}

// TagCache handles Redis operations with tag-based invalidation
type TagCache struct {
	redis   *redis.Client
	options TagCacheOptions
}

type TagCacheOptions struct {
	DefaultTimeout int
	DataPrefix     string
	TagPrefix      string
}

// NewTagCache creates a new TagCache instance
func NewTagCache(redisClient *redis.Client) *TagCache {
	return &TagCache{
		redis: redisClient,
		options: TagCacheOptions{
			DefaultTimeout: 3600, // 1 hour
			DataPrefix:     "tagcache:data:",
			TagPrefix:      "tagcache:tag:",
		},
	}
}

// Set stores data with associated tags
func (tc *TagCache) Set(ctx context.Context, key string, data CachedData, tags []string, timeout *int) error {
	pipe := tc.redis.Pipeline()

	// Add the key to each of the tag sets
	for _, tag := range tags {
		pipe.SAdd(ctx, tc.options.TagPrefix+tag, key)
	}

	// Serialize the data
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	// Set the data with timeout
	expiration := time.Duration(tc.options.DefaultTimeout) * time.Second
	if timeout != nil {
		expiration = time.Duration(*timeout) * time.Second
	}

	pipe.Set(ctx, tc.options.DataPrefix+key, jsonData, expiration)

	_, err = pipe.Exec(ctx)
	return err
}

// Get retrieves data by keys
func (tc *TagCache) Get(ctx context.Context, keys ...string) ([]CachedData, error) {
	dataKeys := make([]string, len(keys))
	for i, key := range keys {
		dataKeys[i] = tc.options.DataPrefix + key
	}

	results, err := tc.redis.MGet(ctx, dataKeys...).Result()
	if err != nil {
		return nil, err
	}

	cachedData := make([]CachedData, len(results))
	for i, result := range results {
		if result == nil {
			cachedData[i] = nil
			continue
		}

		var data CachedData
		if err := json.Unmarshal([]byte(result.(string)), &data); err != nil {
			cachedData[i] = result
		} else {
			cachedData[i] = data
		}
	}

	// Return single element for single key requests (matching Node.js behavior)
	if len(cachedData) == 1 {
		return []CachedData{cachedData[0]}, nil
	}

	return cachedData, nil
}

// Invalidate removes all data associated with the given tags
func (tc *TagCache) Invalidate(ctx context.Context, tags ...string) error {
	// Get all keys associated with all tags
	var allKeys []string
	for _, tag := range tags {
		keys, err := tc.redis.SMembers(ctx, tc.options.TagPrefix+tag).Result()
		if err != nil {
			return err
		}
		allKeys = append(allKeys, keys...)
	}

	if len(allKeys) == 0 && len(tags) == 0 {
		return nil
	}

	pipe := tc.redis.Pipeline()

	// Delete all data keys
	for _, key := range allKeys {
		pipe.Del(ctx, tc.options.DataPrefix+key)
	}

	// Delete all tag keys
	for _, tag := range tags {
		pipe.Del(ctx, tc.options.TagPrefix+tag)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// InvalidateAll removes all cache entries matching the pattern
func (tc *TagCache) InvalidateAll(ctx context.Context, afterPrefixPattern string) error {
	if afterPrefixPattern == "" {
		afterPrefixPattern = "*"
	} else {
		afterPrefixPattern = afterPrefixPattern + "*"
	}

	dataPattern := tc.options.DataPrefix + afterPrefixPattern
	tagPattern := tc.options.TagPrefix + afterPrefixPattern

	if err := tc.invalidateByMatch(ctx, dataPattern); err != nil {
		return err
	}

	return tc.invalidateByMatch(ctx, tagPattern)
}

func (tc *TagCache) invalidateByMatch(ctx context.Context, pattern string) error {
	var cursor uint64
	var keys []string

	for {
		var scanKeys []string
		var err error
		scanKeys, cursor, err = tc.redis.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}

		keys = append(keys, scanKeys...)

		if cursor == 0 {
			break
		}
	}

	if len(keys) == 0 {
		return nil
	}

	pipe := tc.redis.Pipeline()
	for _, key := range keys {
		pipe.Del(ctx, key)
	}

	_, err := pipe.Exec(ctx)
	return err
}
