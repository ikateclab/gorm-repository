package testcontainers

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	redisContainer "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
	postgresDriver "gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DatabaseSetup represents a database setup with cleanup function
type DatabaseSetup struct {
	DB      *gorm.DB
	Cleanup func()
}

// RedisSetup represents a Redis setup with cleanup function
type RedisSetup struct {
	Client  *redis.Client
	Cleanup func()
}

// SetupTestDatabase creates a test database using either SQLite in-memory or PostgreSQL test container
// Use testing.Short() to determine which database to use:
// - go test -short: uses SQLite in-memory (faster)
// - go test: uses PostgreSQL test container (more realistic)
func SetupTestDatabase(t *testing.T, models ...interface{}) DatabaseSetup {
	if testing.Short() {
		return setupSQLiteDatabase(t, models...)
	}
	return setupPostgreSQLDatabase(t, models...)
}

// SetupTestRedis creates a Redis test container
func SetupTestRedis(t *testing.T) RedisSetup {
	ctx := context.Background()

	// Start Redis test container
	container, err := redisContainer.Run(ctx,
		"redis:7-alpine",
		testcontainers.WithWaitStrategy(wait.ForLog("Ready to accept connections")),
	)
	require.NoError(t, err, "Failed to start Redis container")

	// Get connection details
	host, err := container.Host(ctx)
	require.NoError(t, err, "Failed to get Redis host")

	port, err := container.MappedPort(ctx, "6379")
	require.NoError(t, err, "Failed to get Redis port")

	// Create Redis client
	client := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%s", host, port.Port()),
	})

	// Test Redis connection
	_, err = client.Ping(ctx).Result()
	require.NoError(t, err, "Failed to ping Redis")

	cleanup := func() {
		client.Close()
		if err := container.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate Redis container: %v", err)
		}
	}

	return RedisSetup{
		Client:  client,
		Cleanup: cleanup,
	}
}

func setupSQLiteDatabase(t *testing.T, models ...interface{}) DatabaseSetup {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "Failed to connect to SQLite database")

	if len(models) > 0 {
		err = db.AutoMigrate(models...)
		require.NoError(t, err, "Failed to migrate database")
	}

	return DatabaseSetup{
		DB:      db,
		Cleanup: func() {}, // No cleanup needed for in-memory SQLite
	}
}

func setupPostgreSQLDatabase(t *testing.T, models ...interface{}) DatabaseSetup {
	ctx := context.Background()

	container, err := postgres.Run(ctx,
		"postgres:15-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
	)
	require.NoError(t, err, "Failed to start PostgreSQL container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "Failed to get connection string")

	db, err := gorm.Open(postgresDriver.Open(connStr), &gorm.Config{})
	require.NoError(t, err, "Failed to connect to PostgreSQL")

	if len(models) > 0 {
		err = db.AutoMigrate(models...)
		require.NoError(t, err, "Failed to migrate database")
	}

	cleanup := func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate PostgreSQL container: %v", err)
		}
	}

	return DatabaseSetup{
		DB:      db,
		Cleanup: cleanup,
	}
}
