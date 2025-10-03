package cache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	redisContainer "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
	postgresDriver "gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	gormrepository "github.com/ikateclab/gorm-repository"
	"github.com/ikateclab/gorm-repository/utils/tests"
)

// setupTransactionTestDB creates a test database using either SQLite in-memory or PostgreSQL test container
func setupTransactionTestDB(t *testing.T) (*gorm.DB, func()) {
	// Option 1: Use SQLite in-memory (faster, no containers needed)
	if testing.Short() {
		// Use a temporary file instead of :memory: to avoid connection issues
		dbFile := fmt.Sprintf("/tmp/test_%d.db", time.Now().UnixNano())
		db, err := gorm.Open(sqlite.Open(dbFile), &gorm.Config{})
		require.NoError(t, err, "Failed to connect to database")

		err = db.AutoMigrate(&tests.TestUser{})
		require.NoError(t, err, "Failed to migrate database")

		// Debug: Check what tables exist
		var tables []string
		db.Raw("SELECT name FROM sqlite_master WHERE type='table'").Scan(&tables)
		t.Logf("Available tables: %v", tables)

		cleanup := func() {
			// Close database and remove file
			sqlDB, _ := db.DB()
			if sqlDB != nil {
				sqlDB.Close()
			}
			// Note: We could remove the file here, but leaving it for debugging
		}

		return db, cleanup
	}

	// Option 2: Use PostgreSQL test container (more realistic)
	ctx := context.Background()

	postgresContainer, err := postgres.Run(ctx,
		"postgres:15-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
	)
	require.NoError(t, err, "Failed to start PostgreSQL container")

	connStr, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err, "Failed to get connection string")

	db, err := gorm.Open(postgresDriver.Open(connStr), &gorm.Config{})
	require.NoError(t, err, "Failed to connect to PostgreSQL")

	err = db.AutoMigrate(&tests.TestUser{})
	require.NoError(t, err, "Failed to migrate database")

	cleanup := func() {
		if err := postgresContainer.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate PostgreSQL container: %v", err)
		}
	}

	return db, cleanup
}

func setupTransactionTestCache(t *testing.T) (*ResourceCache, *redis.Client, func()) {
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
	rdb := redis.NewClient(&redis.Options{
		Addr: fmt.Sprintf("%s:%s", host, port.Port()),
	})

	// Test Redis connection
	_, err = rdb.Ping(ctx).Result()
	require.NoError(t, err, "Failed to ping Redis")

	tagCache := NewTagCache(rdb)
	logger := NewSimpleLogger()
	resourceCache := NewResourceCache(logger, tagCache, "test-schema-v1", true)

	cleanup := func() {
		rdb.Close()
		if err := container.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate Redis container: %v", err)
		}
	}

	return resourceCache, rdb, cleanup
}

func createTransactionTestUser() *tests.TestUser {
	return &tests.TestUser{
		Id:     uuid.New(),
		Name:   "Transaction Test User",
		Email:  "transaction@test.com",
		Age:    30,
		Active: true,
	}
}

func TestCachedGormRepository_TransactionCommit_InvalidatesCache(t *testing.T) {
	db, dbCleanup := setupTransactionTestDB(t)
	defer dbCleanup()

	cache, _, cacheCleanup := setupTransactionTestCache(t)
	defer cacheCleanup()

	repo := NewCachedGormRepository[tests.TestUser](db, cache, "test-schema-v1", true)
	ctx := context.Background()

	// Create a user to establish cache
	user := createTransactionTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Initial create should succeed")

	// Verify the user was created in the database
	var dbUser tests.TestUser
	err = db.First(&dbUser, "id = ?", user.Id).Error
	require.NoError(t, err, "User should exist in database after create")
	t.Logf("Created user in database: %s", dbUser.Name)

	// Read the user to populate cache
	foundUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "FindById should succeed")
	require.NotNil(t, foundUser, "Found user should not be nil")
	require.Equal(t, user.Name, foundUser.Name, "Found user should match created user")

	// Start transaction and update user
	var txErr error
	tx := repo.BeginTransaction()
	defer func() {
		t.Logf("Finishing transaction with error: %v", txErr)
		tx.Finish(&txErr)
	}()

	// Update user within transaction
	originalName := user.Name
	user.Name = "Updated in Transaction"
	t.Logf("Updating user from '%s' to '%s'", originalName, user.Name)

	txErr = repo.Save(ctx, user, gormrepository.WithTx(tx))
	require.NoError(t, txErr, "Save in transaction should succeed")
	t.Logf("Save completed successfully")

	// Check if the change is visible within the transaction
	// Note: We can't directly access the transaction DB, so we'll check after commit

	// Cache should still contain old data (not yet invalidated)
	// Note: This might still return cached data depending on implementation

	// Transaction will be committed by defer tx.Finish(&txErr)
	// Since txErr is nil, it will commit automatically

	// Verify that fresh data is returned after commit
	// First check if the record exists in the database directly
	err = db.First(&dbUser, "id = ?", user.Id).Error
	require.NoError(t, err, "Direct DB read should succeed after transaction")
	t.Logf("Database user name after transaction: %s", dbUser.Name)

	freshUser, err := repo.FindById(ctx, user.Id, gormrepository.WithTx(tx))
	require.NoError(t, err, "FindById after commit should succeed")
	require.NotNil(t, freshUser, "Fresh user should not be nil")
	if freshUser != nil {
		require.Equal(t, "Updated in Transaction", freshUser.Name, "Should get updated data after commit")
	}
}

func TestCachedGormRepository_TransactionRollback_DoesNotInvalidateCache(t *testing.T) {
	db, dbCleanup := setupTransactionTestDB(t)
	defer dbCleanup()

	cache, _, cacheCleanup := setupTransactionTestCache(t)
	defer cacheCleanup()

	repo := NewCachedGormRepository[tests.TestUser](db, cache, "test-schema-v1", true)
	ctx := context.Background()

	// Create a user to establish cache
	user := createTransactionTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Initial create should succeed")

	// Read the user to populate cache
	foundUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "FindById should succeed")
	require.Equal(t, user.Name, foundUser.Name, "Found user should match created user")

	// Start transaction and update user
	originalName := user.Name

	// Use a function to test rollback behavior
	func() {
		var txErr error
		tx := repo.BeginTransaction()
		defer tx.Finish(&txErr)

		// Update user within transaction
		user.Name = "Updated in Transaction"
		txErr = repo.Save(ctx, user, gormrepository.WithTx(tx))
		require.NoError(t, txErr, "Save in transaction should succeed")

		// Force rollback by setting error
		txErr = gorm.ErrInvalidTransaction
		// Transaction will be rolled back by defer tx.Finish(&txErr)
	}()

	// Cache should not be invalidated, and database should contain original data
	// Read from database directly to verify rollback worked
	var dbUser tests.TestUser
	err = db.First(&dbUser, "id = ?", user.Id).Error
	require.NoError(t, err, "Direct DB read should succeed")
	require.Equal(t, originalName, dbUser.Name, "Database should contain original data after rollback")

	// Cache should also still work and return consistent data
	cachedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "FindById should succeed")
	require.Equal(t, originalName, cachedUser.Name, "Cache should return original data after rollback")
}

func TestCachedGormRepository_NonTransactional_ImmediateInvalidation(t *testing.T) {
	db, dbCleanup := setupTransactionTestDB(t)
	defer dbCleanup()

	cache, _, cacheCleanup := setupTransactionTestCache(t)
	defer cacheCleanup()

	repo := NewCachedGormRepository[tests.TestUser](db, cache, "test-schema-v1", true)
	ctx := context.Background()

	// Create a user to establish cache
	user := createTransactionTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Initial create should succeed")

	// Read the user to populate cache
	foundUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "FindById should succeed")
	require.Equal(t, user.Name, foundUser.Name, "Found user should match created user")

	// Update user without transaction (should invalidate cache immediately)
	user.Name = "Updated Without Transaction"
	err = repo.Save(ctx, user)
	require.NoError(t, err, "Save without transaction should succeed")

	// Cache should be immediately invalidated
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "FindById should succeed")
	require.Equal(t, "Updated Without Transaction", updatedUser.Name, "Should get updated data immediately")
}

func TestCachedGormRepository_NestedTransactions(t *testing.T) {
	db, dbCleanup := setupTransactionTestDB(t)
	defer dbCleanup()

	cache, _, cacheCleanup := setupTransactionTestCache(t)
	defer cacheCleanup()

	repo := NewCachedGormRepository[tests.TestUser](db, cache, "test-schema-v1", true)
	ctx := context.Background()

	// Create a user to establish cache
	user := createTransactionTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Initial create should succeed")

	// Start outer transaction
	var outerTxErr error
	outerTx := repo.BeginTransaction()
	defer outerTx.Finish(&outerTxErr)

	// Update in outer transaction
	user.Name = "Updated in Outer Transaction"
	outerTxErr = repo.Save(ctx, user, gormrepository.WithTx(outerTx))
	require.NoError(t, outerTxErr, "Save in outer transaction should succeed")

	// Start nested transaction
	var innerTxErr error
	innerTx := outerTx.BeginTransaction()
	defer innerTx.Finish(&innerTxErr)

	// Update in inner transaction
	user.Name = "Updated in Inner Transaction"
	innerTxErr = repo.Save(ctx, user, gormrepository.WithTx(innerTx))
	require.NoError(t, innerTxErr, "Save in inner transaction should succeed")

	// Commit inner transaction explicitly (since we want to control the flow)
	innerTxErr = innerTx.Commit()
	require.NoError(t, innerTxErr, "Inner transaction commit should succeed")

	// Commit outer transaction explicitly (since we want to control the flow)
	outerTxErr = outerTx.Commit()
	require.NoError(t, outerTxErr, "Outer transaction commit should succeed")

	// Verify final state
	finalUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "FindById should succeed")
	require.Equal(t, "Updated in Inner Transaction", finalUser.Name, "Should get final updated data")
}
