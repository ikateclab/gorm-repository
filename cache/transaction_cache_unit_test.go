package cache

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	gormrepository "github.com/ikateclab/gorm-repository"
	"github.com/ikateclab/gorm-repository/utils/tests"
)

// MockResourceCache implements a simple in-memory cache for testing
type MockResourceCache struct {
	data            map[string]interface{}
	invalidatedTags []string
}

func NewMockResourceCache() *MockResourceCache {
	return &MockResourceCache{
		data:            make(map[string]interface{}),
		invalidatedTags: make([]string, 0),
	}
}

func (m *MockResourceCache) Remember(
	ctx context.Context,
	rawKey RawKey,
	getValue func() (interface{}, error),
	getTags func(interface{}) ([]RawTag, error),
	options *RememberOptions,
) (interface{}, error) {
	// Simple implementation - always call getValue for testing
	return getValue()
}

func (m *MockResourceCache) ForgetByTags(ctx context.Context, rawTags []RawTag) error {
	// Record which tags were invalidated for testing
	for _, tag := range rawTags {
		if tagStr, ok := tag.(string); ok {
			m.invalidatedTags = append(m.invalidatedTags, tagStr)
		}
	}
	return nil
}

func (m *MockResourceCache) GetInvalidatedTags() []string {
	return m.invalidatedTags
}

func (m *MockResourceCache) ClearInvalidatedTags() {
	m.invalidatedTags = make([]string, 0)
}

func setupUnitTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err, "Failed to connect to database")

	err = db.AutoMigrate(&tests.TestUser{})
	require.NoError(t, err, "Failed to migrate database")

	return db
}

func createUnitTestUser() *tests.TestUser {
	id := uuid.New()
	return &tests.TestUser{
		Id:     id,
		Name:   "Unit Test User",
		Email:  fmt.Sprintf("unit-%s@test.com", id.String()[:8]),
		Age:    25,
		Active: true,
	}
}

func TestTransactionAwareCaching_CommitInvalidatesCache(t *testing.T) {
	db := setupUnitTestDB(t)
	mockCache := NewMockResourceCache()

	// Create a cached repository with mock cache
	repo := NewCachedGormRepositoryWithCache[tests.TestUser](db, mockCache, "test-v1", true)

	ctx := context.Background()
	user := createUnitTestUser()

	// Create user outside transaction (should invalidate immediately)
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Create should succeed")

	// Verify cache was invalidated immediately
	require.Greater(t, len(mockCache.GetInvalidatedTags()), 0, "Cache should be invalidated immediately for non-transactional operations")

	// Clear invalidated tags for next test
	mockCache.ClearInvalidatedTags()

	// Use a function to test the defer behavior properly
	func() {
		var txErr error
		tx := repo.BeginTransaction()
		defer tx.Finish(&txErr)

		// Update user within transaction
		user.Name = "Updated in Transaction"
		txErr = repo.Save(ctx, user, gormrepository.WithTx(tx))
		require.NoError(t, txErr, "Save in transaction should succeed")

		// Cache should NOT be invalidated yet (operation is queued)
		require.Equal(t, 0, len(mockCache.GetInvalidatedTags()), "Cache should not be invalidated during transaction")

		// Transaction will be committed by defer tx.Finish(&txErr) when this function exits
	}()

	// Now cache should be invalidated (after the function with defer completed)
	require.Greater(t, len(mockCache.GetInvalidatedTags()), 0, "Cache should be invalidated after transaction commit")
}

func TestTransactionAwareCaching_RollbackDoesNotInvalidateCache(t *testing.T) {
	db := setupUnitTestDB(t)
	mockCache := NewMockResourceCache()

	// Create a cached repository with mock cache
	repo := NewCachedGormRepositoryWithCache[tests.TestUser](db, mockCache, "test-v1", true)

	ctx := context.Background()
	user := createUnitTestUser()

	// Create user outside transaction
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Create should succeed")

	// Clear invalidated tags for clean test
	mockCache.ClearInvalidatedTags()

	// Start transaction and update user
	tx := repo.BeginTransaction()

	// Update user within transaction
	user.Name = "Updated in Transaction"
	err = repo.Save(ctx, user, gormrepository.WithTx(tx))
	require.NoError(t, err, "Save in transaction should succeed")

	// Cache should NOT be invalidated yet
	require.Equal(t, 0, len(mockCache.GetInvalidatedTags()), "Cache should not be invalidated during transaction")

	// Rollback transaction
	err = tx.Rollback()
	require.NoError(t, err, "Transaction rollback should succeed")

	// Cache should still NOT be invalidated (operations were cleared)
	require.Equal(t, 0, len(mockCache.GetInvalidatedTags()), "Cache should not be invalidated after transaction rollback")
}

func TestTransactionAwareCaching_MultipleOperationsInTransaction(t *testing.T) {
	db := setupUnitTestDB(t)
	mockCache := NewMockResourceCache()

	// Create a cached repository with mock cache
	repo := NewCachedGormRepositoryWithCache[tests.TestUser](db, mockCache, "test-v1", true)

	ctx := context.Background()
	user1 := createUnitTestUser()
	user2 := createUnitTestUser()

	// Create users outside transaction
	err := repo.Create(ctx, user1)
	require.NoError(t, err, "Create user1 should succeed")
	err = repo.Create(ctx, user2)
	require.NoError(t, err, "Create user2 should succeed")

	// Clear invalidated tags for clean test
	mockCache.ClearInvalidatedTags()

	// Use a function to test the defer behavior properly
	func() {
		var txErr error
		tx := repo.BeginTransaction()
		defer tx.Finish(&txErr)

		// Multiple updates in transaction
		user1.Name = "Updated User 1"
		txErr = repo.Save(ctx, user1, gormrepository.WithTx(tx))
		require.NoError(t, txErr, "Save user1 in transaction should succeed")

		user2.Name = "Updated User 2"
		txErr = repo.Save(ctx, user2, gormrepository.WithTx(tx))
		require.NoError(t, txErr, "Save user2 in transaction should succeed")

		// Cache should still not be invalidated
		require.Equal(t, 0, len(mockCache.GetInvalidatedTags()), "Cache should not be invalidated during transaction")

		// Transaction will be committed by defer tx.Finish(&txErr) when this function exits
	}()

	// Now cache should be invalidated (multiple operations should be executed)
	require.Greater(t, len(mockCache.GetInvalidatedTags()), 0, "Cache should be invalidated after transaction commit")
}

func TestTransactionCacheManager_QueueAndExecute(t *testing.T) {
	manager := gormrepository.NewTransactionCacheManager()
	ctx := context.Background()

	executed := false
	operation := func(ctx context.Context) error {
		executed = true
		return nil
	}

	// Queue operation
	manager.QueueOperation(operation)
	require.False(t, executed, "Operation should not be executed immediately")

	// Execute on commit
	manager.ExecuteOnCommit(ctx)
	require.True(t, executed, "Operation should be executed on commit")
}

func TestTransactionCacheManager_ClearOnRollback(t *testing.T) {
	manager := gormrepository.NewTransactionCacheManager()
	ctx := context.Background()

	executed := false
	operation := func(ctx context.Context) error {
		executed = true
		return nil
	}

	// Queue operation
	manager.QueueOperation(operation)
	require.False(t, executed, "Operation should not be executed immediately")

	// Clear on rollback
	manager.ClearOnRollback()

	// Execute on commit should do nothing now
	manager.ExecuteOnCommit(ctx)
	require.False(t, executed, "Operation should not be executed after rollback")
}
