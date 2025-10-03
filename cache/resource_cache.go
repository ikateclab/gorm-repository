package cache

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
)

// SimpleLogger provides a basic logger implementation
type SimpleLogger struct{}

func (l *SimpleLogger) Log(message string) {
	fmt.Printf("[Cache] %s\n", message)
}

// NewSimpleLogger creates a new simple logger
func NewSimpleLogger() *SimpleLogger {
	return &SimpleLogger{}
}

// ResourceCache defines the interface for cache operations
type ResourceCacheInterface interface {
	Remember(
		ctx context.Context,
		rawKey RawKey,
		getValue func() (interface{}, error),
		getTags func(interface{}) ([]RawTag, error),
		options *RememberOptions,
	) (interface{}, error)
	ForgetByTags(ctx context.Context, rawTags []RawTag) error
}

// ResourceCache is the main cache interface
type ResourceCache struct {
	logger          Logger
	tagCache        *TagCache
	dbSchemaVersion string
	debugEnabled    bool
	hitCount        int64
	missCount       int64
	mu              sync.RWMutex

	minTimeout int
	maxTimeout int
}

// NewResourceCache creates a new ResourceCache instance
func NewResourceCache(logger Logger, tagCache *TagCache, dbSchemaVersion string, debugEnabled bool) *ResourceCache {
	return &ResourceCache{
		logger:          logger,
		tagCache:        tagCache,
		dbSchemaVersion: dbSchemaVersion,
		debugEnabled:    debugEnabled,
		minTimeout:      3600,     // 1 hour
		maxTimeout:      3 * 3600, // 3 hours
	}
}

// PrepareKey creates a cache key from raw key input
func (rc *ResourceCache) PrepareKey(rawKey RawKey, dontHashKey bool) string {
	dbVersion := rc.dbSchemaVersion

	var stringKey string
	if str, ok := rawKey.(string); ok {
		stringKey = str
	} else {
		jsonBytes, _ := json.Marshal(rawKey)
		stringKey = string(jsonBytes)
	}

	if dontHashKey {
		return fmt.Sprintf("%s:%s", dbVersion, stringKey)
	}

	// Generate hash similar to Node.js md5
	hash := fmt.Sprintf("%x", md5.Sum([]byte(stringKey)))

	var modelName string
	if slice, ok := rawKey.([]interface{}); ok && len(slice) > 0 {
		if str, ok := slice[0].(string); ok {
			modelName = str
		}
	}
	if modelName == "" {
		modelName = "no-model"
	}

	return fmt.Sprintf("%s:%s:%s", dbVersion, modelName, hash)
}

// PrepareTag creates a cache tag from raw tag input
func (rc *ResourceCache) PrepareTag(rawTag RawTag) string {
	dbVersion := rc.dbSchemaVersion

	var stringTag string
	if str, ok := rawTag.(string); ok {
		stringTag = str
	} else {
		jsonBytes, _ := json.Marshal(rawTag)
		stringTag = string(jsonBytes)
	}

	return fmt.Sprintf("%s:%s", dbVersion, stringTag)
}

// SetOptions represents options for Set operation
type SetOptions struct {
	DontHashKey bool
	Timeout     *int
}

// Set stores a value with tags
func (rc *ResourceCache) Set(ctx context.Context, rawKey RawKey, value interface{}, rawTags []RawTag, options *SetOptions) error {
	if options == nil {
		options = &SetOptions{}
	}

	key := rc.PrepareKey(rawKey, options.DontHashKey)
	tags := make([]string, len(rawTags))
	for i, rawTag := range rawTags {
		tags[i] = rc.PrepareTag(rawTag)
	}

	timeout := options.Timeout
	if timeout == nil {
		randomTimeout := rc.getRandomTimeout()
		timeout = &randomTimeout
	}

	cacheValue := Value{
		RawKey: rawKey,
		Value:  value,
	}

	return rc.tagCache.Set(ctx, key, cacheValue, tags, timeout)
}

// Get retrieves a cached value
func (rc *ResourceCache) Get(ctx context.Context, rawKey RawKey) (interface{}, error) {
	key := rc.PrepareKey(rawKey, false)
	results, err := rc.tagCache.Get(ctx, key)
	if err != nil {
		return nil, err
	}

	if len(results) == 0 || results[0] == nil {
		return nil, nil
	}

	// Extract value from CacheValue structure
	if cacheValue, ok := results[0].(map[string]interface{}); ok {
		return cacheValue["value"], nil
	}

	return results[0], nil
}

// RememberOptions represents options for Remember operation
type RememberOptions struct {
	DontHashKey bool
	Timeout     *int
	SkipCache   bool
}

// Remember implements the cache-aside pattern
func (rc *ResourceCache) Remember(
	ctx context.Context,
	rawKey RawKey,
	getValue func() (interface{}, error),
	getTags func(interface{}) ([]RawTag, error),
	options *RememberOptions,
) (interface{}, error) {
	if !rc.isEnabled() {
		return getValue()
	}

	if options == nil {
		options = &RememberOptions{}
	}

	key := rc.PrepareKey(rawKey, options.DontHashKey)

	if !options.SkipCache {
		// Try to get from cache first
		cacheValue, err := rc.Get(ctx, rawKey)
		if err != nil {
			return nil, err
		}

		if cacheValue != nil {
			if rc.debugEnabled {
				hitRatio := rc.incrementHitCount()
				rc.log(fmt.Sprintf("Cache hit: %s. (%.2f hit ratio)", key, hitRatio))
			}
			return cacheValue, nil
		}
	}

	if rc.debugEnabled {
		hitRatio := rc.incrementMissCount()
		rc.log(fmt.Sprintf("Cache miss: %s. (%.2f hit ratio)", key, hitRatio))
	}

	// Get value from source
	value, err := getValue()

	if err != nil {
		return nil, err
	}

	if value == nil {
		return value, nil
	}

	// Gormrepository always return a pointer, so we need to dereference it
	if reflect.TypeOf(value).Kind() == reflect.Ptr {
		value = reflect.ValueOf(value).Elem().Interface()
	}

	// Get tags
	var rawTags []RawTag
	if getTags != nil {
		rawTags, err = getTags(value)
		if err != nil {
			return nil, err
		}
	}

	if rc.debugEnabled && len(rawTags) > 0 {
		tags := make([]string, len(rawTags))
		for i, rawTag := range rawTags {
			tags[i] = rc.PrepareTag(rawTag)
		}
		rc.log(fmt.Sprintf("Setting cache: %v", tags))
	}

	// Store in cache
	setOptions := &SetOptions{
		DontHashKey: options.DontHashKey,
		Timeout:     options.Timeout,
	}
	if setOptions.Timeout == nil {
		randomTimeout := rc.getRandomTimeout()
		setOptions.Timeout = &randomTimeout
	}

	err = rc.Set(ctx, rawKey, value, rawTags, setOptions)
	if err != nil {
		// Log error but don't fail the request
		rc.log(fmt.Sprintf("Failed to set cache: %v", err))
	}

	return value, nil
}

// ForgetByTags invalidates cache entries by tags
func (rc *ResourceCache) ForgetByTags(ctx context.Context, rawTags []RawTag) error {
	if !rc.isEnabled() {
		return nil
	}

	// Remove duplicates
	uniqueTags := make(map[string]bool)
	for _, rawTag := range rawTags {
		tag := rc.PrepareTag(rawTag)
		uniqueTags[tag] = true
	}

	tags := make([]string, 0, len(uniqueTags))
	for tag := range uniqueTags {
		tags = append(tags, tag)
	}

	rc.log(fmt.Sprintf("Forgetting tags: %v", tags))
	return rc.tagCache.Invalidate(ctx, tags...)
}

func (rc *ResourceCache) getRandomTimeout() int {
	return rand.Intn(rc.maxTimeout-rc.minTimeout) + rc.minTimeout
}

func (rc *ResourceCache) isEnabled() bool {
	// You can implement this based on your config
	return true
	// return rc.config.Get("resourceCacheEnabled").(bool)
}

func (rc *ResourceCache) incrementHitCount() float64 {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.hitCount++
	return rc.getHitRatio()
}

func (rc *ResourceCache) incrementMissCount() float64 {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.missCount++
	return rc.getHitRatio()
}

func (rc *ResourceCache) getHitRatio() float64 {
	total := rc.hitCount + rc.missCount
	if total == 0 {
		return 0
	}
	return float64(rc.hitCount) / float64(total)
}

func (rc *ResourceCache) log(message string) {
	if !rc.debugEnabled {
		return
	}
	if rc.logger != nil {
		rc.logger.Log(message)
	} else {
		fmt.Printf("[ResourceCache] %s\n", message)
	}
}
