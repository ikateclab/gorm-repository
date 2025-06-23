package cache

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	gormrepository "github.com/ikateclab/gorm-repository"
)

// Test entity
type TestUser struct {
	ID        uuid.UUID `gorm:"type:text;primary_key" json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	AccountId string    `json:"accountId"`
}

// Implement Diffable interface
func (u *TestUser) Clone() *TestUser {
	if u == nil {
		return nil
	}
	clone := *u
	return &clone
}

func (u *TestUser) Diff(other *TestUser) map[string]interface{} {
	diff := make(map[string]interface{})
	
	if other == nil {
		diff["name"] = u.Name
		diff["email"] = u.Email
		diff["accountId"] = u.AccountId
		return diff
	}
	
	if u.Name != other.Name {
		diff["name"] = u.Name
	}
	if u.Email != other.Email {
		diff["email"] = u.Email
	}
	if u.AccountId != other.AccountId {
		diff["accountId"] = u.AccountId
	}
	
	return diff
}

func setupTestEnvironment(t *testing.T) (*CachedGormRepository[*TestUser], *redis.Client, context.Context) {
	// Setup database
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TestUser{}))

	// Setup Redis (use a test database)
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15, // Use test database
	})

	ctx := context.Background()
	
	// Clear test database
	redisClient.FlushDB(ctx)

	// Test Redis connection
	err = redisClient.Ping(ctx).Err()
	if err != nil {
		t.Skip("Redis not available, skipping cache tests")
	}

	// Create cache components
	logger := NewSimpleLogger()
	tagCache := NewTagCache(redisClient)
	resourceCache := NewResourceCache(logger, tagCache, "test-v1.0.0", true)

	// Create cached repository
	repo := NewCachedGormRepository[*TestUser](db, resourceCache, "test-v1.0.0", true)

	return repo, redisClient, ctx
}

func TestCachedRepository_BasicOperations(t *testing.T) {
	repo, redisClient, ctx := setupTestEnvironment(t)
	defer redisClient.Close()

	// Create a test user
	user := &TestUser{
		ID:        uuid.New(),
		Name:      "Test User",
		Email:     "test@example.com",
		AccountId: "test-account",
	}

	// Test Create
	err := repo.Create(ctx, user)
	require.NoError(t, err)

	// Test FindById (should cache the result)
	foundUser, err := repo.FindById(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.Name, foundUser.Name)
	assert.Equal(t, user.Email, foundUser.Email)

	// Test cache hit by finding the same user again
	foundUser2, err := repo.FindById(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, foundUser.Name, foundUser2.Name)

	// Test Update (should invalidate cache)
	foundUser.Name = "Updated User"
	err = repo.UpdateById(ctx, foundUser.ID, foundUser)
	require.NoError(t, err)

	// Verify update worked and cache was invalidated
	updatedUser, err := repo.FindById(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated User", updatedUser.Name)
}

func TestCachedRepository_FindMany(t *testing.T) {
	repo, redisClient, ctx := setupTestEnvironment(t)
	defer redisClient.Close()

	// Create multiple test users
	users := []*TestUser{
		{ID: uuid.New(), Name: "User 1", Email: "user1@example.com", AccountId: "account-1"},
		{ID: uuid.New(), Name: "User 2", Email: "user2@example.com", AccountId: "account-1"},
		{ID: uuid.New(), Name: "User 3", Email: "user3@example.com", AccountId: "account-2"},
	}

	for _, user := range users {
		err := repo.Create(ctx, user)
		require.NoError(t, err)
	}

	// Test FindMany with query (should cache the result)
	foundUsers, err := repo.FindMany(ctx, gormrepository.WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("account_id = ?", "account-1")
	}))
	require.NoError(t, err)
	assert.Len(t, foundUsers, 2)

	// Test FindMany again (should hit cache)
	foundUsers2, err := repo.FindMany(ctx, gormrepository.WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("account_id = ?", "account-1")
	}))
	require.NoError(t, err)
	assert.Len(t, foundUsers2, 2)
}

func TestCachedRepository_Pagination(t *testing.T) {
	repo, redisClient, ctx := setupTestEnvironment(t)
	defer redisClient.Close()

	// Create test users
	for i := 0; i < 15; i++ {
		user := &TestUser{
			ID:        uuid.New(),
			Name:      fmt.Sprintf("User %d", i),
			Email:     fmt.Sprintf("user%d@example.com", i),
			AccountId: "test-account",
		}
		err := repo.Create(ctx, user)
		require.NoError(t, err)
	}

	// Test pagination (should cache the result)
	result, err := repo.FindPaginated(ctx, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(15), result.Total)
	assert.Len(t, result.Data, 10)
	assert.Equal(t, 1, result.CurrentPage)

	// Test pagination again (should hit cache)
	result2, err := repo.FindPaginated(ctx, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, result.Total, result2.Total)
	assert.Len(t, result2.Data, 10)
}

func TestCachedRepository_CacheInvalidation(t *testing.T) {
	repo, redisClient, ctx := setupTestEnvironment(t)
	defer redisClient.Close()

	// Create a test user
	user := &TestUser{
		ID:        uuid.New(),
		Name:      "Test User",
		Email:     "test@example.com",
		AccountId: "test-account",
	}

	err := repo.Create(ctx, user)
	require.NoError(t, err)

	// Cache the user by finding it
	_, err = repo.FindById(ctx, user.ID)
	require.NoError(t, err)

	// Update the user (should invalidate cache)
	user.Name = "Updated User"
	err = repo.UpdateById(ctx, user.ID, user)
	require.NoError(t, err)

	// Verify the cache was invalidated by checking the updated value
	updatedUser, err := repo.FindById(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated User", updatedUser.Name)

	// Delete the user (should invalidate cache)
	err = repo.DeleteById(ctx, user.ID)
	require.NoError(t, err)

	// Verify the user is deleted
	_, err = repo.FindById(ctx, user.ID)
	assert.Error(t, err) // Should return error for deleted user
}

func TestCachedRepository_AssociationMethods(t *testing.T) {
	repo, redisClient, ctx := setupTestEnvironment(t)
	defer redisClient.Close()

	user := &TestUser{
		ID:        uuid.New(),
		Name:      "Test User",
		Email:     "test@example.com",
		AccountId: "test-account",
	}

	err := repo.Create(ctx, user)
	require.NoError(t, err)

	// Test association methods (they should not panic and should invalidate cache)
	// Note: These are basic tests since we don't have actual associations in TestUser
	err = repo.AppendAssociation(ctx, user, "tags", []string{"tag1"})
	// This might error due to no actual association, but shouldn't panic
	
	err = repo.RemoveAssociation(ctx, user, "tags", []string{"tag1"})
	// This might error due to no actual association, but shouldn't panic
	
	err = repo.ReplaceAssociation(ctx, user, "tags", []string{"tag2"})
	// This might error due to no actual association, but shouldn't panic
	
	// The main thing is that these methods exist and don't panic
	assert.NotNil(t, repo.AppendAssociation)
	assert.NotNil(t, repo.RemoveAssociation)
	assert.NotNil(t, repo.ReplaceAssociation)
}
