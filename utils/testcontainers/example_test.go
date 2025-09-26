package testcontainers

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/ikateclab/gorm-repository/utils/tests"
)

// Example test showing how to use the test container helpers
func TestDatabaseSetup_Example(t *testing.T) {
	// Setup database with auto-migration
	dbSetup := SetupTestDatabase(t, &tests.TestUser{})
	defer dbSetup.Cleanup()

	db := dbSetup.DB

	// Create a test user
	user := &tests.TestUser{
		Id:     uuid.New(),
		Name:   "Test User",
		Email:  "test@example.com",
		Age:    25,
		Active: true,
	}

	// Test database operations
	err := db.Create(user).Error
	require.NoError(t, err, "Failed to create user")

	// Find the user
	var foundUser tests.TestUser
	err = db.First(&foundUser, "id = ?", user.Id).Error
	require.NoError(t, err, "Failed to find user")
	require.Equal(t, user.Name, foundUser.Name, "User name should match")
	require.Equal(t, user.Email, foundUser.Email, "User email should match")
}

// Example test showing how to use Redis test container
func TestRedisSetup_Example(t *testing.T) {
	// Setup Redis
	redisSetup := SetupTestRedis(t)
	defer redisSetup.Cleanup()

	client := redisSetup.Client
	ctx := context.Background()

	// Test Redis operations
	err := client.Set(ctx, "test-key", "test-value", 0).Err()
	require.NoError(t, err, "Failed to set Redis key")

	val, err := client.Get(ctx, "test-key").Result()
	require.NoError(t, err, "Failed to get Redis key")
	require.Equal(t, "test-value", val, "Redis value should match")
}

// Example test showing how to use both database and Redis together
func TestCombinedSetup_Example(t *testing.T) {
	// Setup database
	dbSetup := SetupTestDatabase(t, &tests.TestUser{})
	defer dbSetup.Cleanup()

	// Setup Redis
	redisSetup := SetupTestRedis(t)
	defer redisSetup.Cleanup()

	db := dbSetup.DB
	redisClient := redisSetup.Client
	ctx := context.Background()

	// Create a user in database
	user := &tests.TestUser{
		Id:     uuid.New(),
		Name:   "Combined Test User",
		Email:  "combined@example.com",
		Age:    30,
		Active: true,
	}

	err := db.Create(user).Error
	require.NoError(t, err, "Failed to create user in database")

	// Cache user ID in Redis
	cacheKey := "user:" + user.Id.String()
	err = redisClient.Set(ctx, cacheKey, user.Name, 0).Err()
	require.NoError(t, err, "Failed to cache user in Redis")

	// Verify both database and cache
	var dbUser tests.TestUser
	err = db.First(&dbUser, "id = ?", user.Id).Error
	require.NoError(t, err, "Failed to find user in database")

	cachedName, err := redisClient.Get(ctx, cacheKey).Result()
	require.NoError(t, err, "Failed to get cached user name")
	require.Equal(t, user.Name, cachedName, "Cached name should match")
	require.Equal(t, dbUser.Name, cachedName, "Database and cache should be consistent")
}
