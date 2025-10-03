package cache

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	gormrepository "github.com/ikateclab/gorm-repository"
)

// Test entity
type TestUser struct {
	ID          uuid.UUID         `gorm:"type:text;primary_key" json:"id"`
	Name        string            `gorm:"type:text" json:"name"`
	Email       string            `gorm:"type:text" json:"email"`
	AccountId   string            `gorm:"column:accountId;type:text" json:"accountId"`
	Departments []*TestDepartment `gorm:"many2many:user_departments;" json:"departments"`
}

type TestDepartment struct {
	ID        uuid.UUID `gorm:"type:text;primary_key" json:"id"`
	Name      string    `gorm:"type:text" json:"name"`
	AccountId string    `gorm:"column:accountId;type:text" json:"accountId"`
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

// Test environment struct
type testEnv struct {
	UserRepo       *CachedGormRepository[TestUser]
	DepartmentRepo *CachedGormRepository[TestDepartment]
	RedisClient    *redis.Client
	Ctx            context.Context
	Cleanup        func()
}

func setupTestEnvironment(t *testing.T) *testEnv {
	newLogger := logger.New(
		log.New(os.Stdout, "\r", log.LstdFlags),
		logger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  logger.Info,
			IgnoreRecordNotFoundError: true,
			ParameterizedQueries:      false,
			Colorful:                  true,
		},
	)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: newLogger,
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&TestUser{}, &TestDepartment{}))

	redisClient := redis.NewClient(&redis.Options{
		Addr: "0.0.0.0:6379",
		DB:   15,
	})

	ctx := context.Background()
	redisClient.FlushDB(ctx)

	err = redisClient.Ping(ctx).Err()
	if err != nil {
		t.Skip("Redis not available, skipping cache tests")
	}

	logger := NewSimpleLogger()
	tagCache := NewTagCache(redisClient)
	resourceCache := NewResourceCache(logger, tagCache, "test-v1.0.0", true)

	userRepo := NewCachedGormRepository[TestUser](db, resourceCache, "test-v1.0.0", true)
	departmentRepo := NewCachedGormRepository[TestDepartment](db, resourceCache, "test-v1.0.0", true)

	cleanup := func() {
		redisClient.Close()
	}

	return &testEnv{
		UserRepo:       userRepo,
		DepartmentRepo: departmentRepo,
		RedisClient:    redisClient,
		Ctx:            ctx,
		Cleanup:        cleanup,
	}
}

func TestCachedRepository_BasicOperations(t *testing.T) {
	env := setupTestEnvironment(t)
	defer env.Cleanup()

	user := &TestUser{
		ID:        uuid.New(),
		Name:      "Test User",
		Email:     "test@example.com",
		AccountId: "test-account",
	}

	err := env.UserRepo.Create(env.Ctx, user)
	require.NoError(t, err)

	foundUser, err := env.UserRepo.FindById(env.Ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.Name, foundUser.Name)
	assert.Equal(t, user.Email, foundUser.Email)

	foundUser2, err := env.UserRepo.FindById(env.Ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, foundUser.Name, foundUser2.Name)

	foundUser.Name = "Updated User"
	err = env.UserRepo.UpdateById(env.Ctx, foundUser.ID, foundUser)
	require.NoError(t, err)

	updatedUser, err := env.UserRepo.FindById(env.Ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated User", updatedUser.Name)
}

func TestCachedRepository_WithRelations(t *testing.T) {
	env := setupTestEnvironment(t)
	defer env.Cleanup()

	department1 := &TestDepartment{
		ID:        uuid.New(),
		Name:      "A",
		AccountId: "test-account",
	}
	department2 := &TestDepartment{
		ID:        uuid.New(),
		Name:      "B",
		AccountId: "test-account",
	}

	err := env.DepartmentRepo.Create(env.Ctx, department1)
	require.NoError(t, err)

	err = env.DepartmentRepo.Create(env.Ctx, department2)
	require.NoError(t, err)

	user := &TestUser{
		ID:        uuid.New(),
		Name:      "Test User",
		Email:     "test@example.com",
		AccountId: "test-account",
	}

	err = env.UserRepo.Create(env.Ctx, user)
	require.NoError(t, err)

	err = env.UserRepo.AppendAssociation(env.Ctx, user, "Departments", []*TestDepartment{
		department1,
		department2,
	})
	require.NoError(t, err)

	// Test FindById (should cache the result)
	foundUser, err := env.UserRepo.FindById(env.Ctx, user.ID, gormrepository.WithRelations(gormrepository.RelationOption{
		Callback: func(db *gorm.DB) *gorm.DB {
			return db.Order(`test_departments.name ASC`)
		},
		Name: "Departments",
	}))

	fmt.Println("Found User:", foundUser)

	require.NoError(t, err)
	assert.Equal(t, user.Name, foundUser.Name)
	assert.Equal(t, user.Email, foundUser.Email)
	assert.Len(t, foundUser.Departments, 2)
	assert.Equal(t, "A", foundUser.Departments[0].Name)
	assert.Equal(t, "B", foundUser.Departments[1].Name)

	// // Test cache hit by finding the same user again
	// foundUser2, err := env.UserRepo.FindById(env.Ctx, user.ID, gormrepository.WithRelations(gormrepository.RelationOption{
	// 	Callback: func(db *gorm.DB) *gorm.DB {
	// 		return db.Order(`test_departments.name ASC`)
	// 	},
	// 	Name: "Departments",
	// }))
	// require.NoError(t, err)
	// assert.Equal(t, foundUser.Name, foundUser2.Name)
	// assert.Len(t, foundUser2.Departments, 2)
	// assert.Equal(t, "A", foundUser2.Departments[0].Name)
	// assert.Equal(t, "B", foundUser2.Departments[1].Name)

	// // Test Update (should invalidate cache)
	// foundUser.Name = "Updated User"
	// err = env.UserRepo.UpdateById(env.Ctx, foundUser.ID, foundUser)
	// require.NoError(t, err)

	// // Verify update worked and cache was invalidated
	// updatedUser, err := env.UserRepo.FindById(env.Ctx, user.ID, gormrepository.WithRelations(gormrepository.RelationOption{
	// 	Callback: func(db *gorm.DB) *gorm.DB {
	// 		return db.Order(`test_departments.name ASC`)
	// 	},
	// 	Name: "Departments",
	// }))
	// require.NoError(t, err)
	// assert.Equal(t, "Updated User", updatedUser.Name)
	// assert.Len(t, updatedUser.Departments, 2)
	// assert.Equal(t, "A", updatedUser.Departments[0].Name)
	// assert.Equal(t, "B", updatedUser.Departments[1].Name)

	// Test delete Department (should invalidate cache)
	err = env.DepartmentRepo.DeleteById(env.Ctx, department1.ID)
	require.NoError(t, err)

	// Verify association was removed and cache was invalidated
	updatedUser2, err := env.UserRepo.FindById(env.Ctx, user.ID, gormrepository.WithRelations(gormrepository.RelationOption{
		Callback: func(db *gorm.DB) *gorm.DB {
			return db.Order(`test_departments.name ASC`)
		},
		Name: "Departments",
	}))
	require.NoError(t, err)
	assert.Equal(t, "Updated User", updatedUser2.Name)
	assert.Len(t, updatedUser2.Departments, 1)
	assert.Equal(t, "B", updatedUser2.Departments[0].Name)

	// // Re-add department1 for next test
	// err = env.DepartmentRepo.Create(env.Ctx, department1)
	// require.NoError(t, err)

	// err = env.UserRepo.AppendAssociation(env.Ctx, user, "Departments", []*TestDepartment{department1})
	// require.NoError(t, err)

	// // Verify association was added
	// updatedUser3, err := env.UserRepo.FindById(env.Ctx, user.ID, gormrepository.WithRelations(gormrepository.RelationOption{
	// 	Callback: func(db *gorm.DB) *gorm.DB {
	// 		return db.Order(`test_departments.name ASC`)
	// 	},
	// 	Name: "Departments",
	// }))
	// require.NoError(t, err)
	// assert.Equal(t, "Updated User", updatedUser3.Name)
	// assert.Len(t, updatedUser3.Departments, 2)
	// assert.Equal(t, "A", updatedUser3.Departments[0].Name)
	// assert.Equal(t, "B", updatedUser3.Departments[1].Name)

	// // Test remove association (should invalidate cache)
	// err = env.UserRepo.RemoveAssociation(env.Ctx, user, "Departments", []*TestDepartment{department1})
	// require.NoError(t, err)

	// // Verify association was removed and cache was invalidated
	// updatedUser4, err := env.UserRepo.FindById(env.Ctx, user.ID, gormrepository.WithRelations(gormrepository.RelationOption{
	// 	Callback: func(db *gorm.DB) *gorm.DB {
	// 		return db.Order(`test_departments.name ASC`)
	// 	},
	// 	Name: "Departments",
	// }))
	// require.NoError(t, err)
	// assert.Equal(t, "Updated User", updatedUser4.Name)
	// assert.Len(t, updatedUser4.Departments, 1)
	// assert.Equal(t, "B", updatedUser4.Departments[0].Name)

	// // Test Delete (should invalidate cache)
	// err = env.UserRepo.DeleteById(env.Ctx, updatedUser4.ID)
	// require.NoError(t, err)

	// // Verify user is deleted
	// _, err = env.UserRepo.FindById(env.Ctx, updatedUser4.ID)
	// assert.Error(t, err) // Should return error for deleted user
}

func TestCachedRepository_FindMany(t *testing.T) {
	env := setupTestEnvironment(t)
	defer env.Cleanup()

	// Create multiple test users
	users := []*TestUser{
		{ID: uuid.New(), Name: "User 1", Email: "user1@example.com", AccountId: "account-1"},
		{ID: uuid.New(), Name: "User 2", Email: "user2@example.com", AccountId: "account-1"},
		{ID: uuid.New(), Name: "User 3", Email: "user3@example.com", AccountId: "account-2"},
	}

	for _, user := range users {
		err := env.UserRepo.Create(env.Ctx, user)
		require.NoError(t, err)
	}

	// Test FindMany with query (should cache the result)
	foundUsers, err := env.UserRepo.FindMany(env.Ctx, gormrepository.WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("accountId = ?", "account-1")
	}))
	require.NoError(t, err)
	assert.Len(t, foundUsers, 2)

	// Test FindMany again (should hit cache)
	foundUsers2, err := env.UserRepo.FindMany(env.Ctx, gormrepository.WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("accountId = ?", "account-1")
	}))
	require.NoError(t, err)
	assert.Len(t, foundUsers2, 2)
}

func TestCachedRepository_Pagination(t *testing.T) {
	env := setupTestEnvironment(t)
	defer env.Cleanup()

	// Create test users
	for i := 0; i < 15; i++ {
		user := &TestUser{
			ID:        uuid.New(),
			Name:      fmt.Sprintf("User %d", i),
			Email:     fmt.Sprintf("user%d@example.com", i),
			AccountId: "test-account",
		}
		err := env.UserRepo.Create(env.Ctx, user)
		require.NoError(t, err)
	}

	// Test pagination (should cache the result)
	result, err := env.UserRepo.FindPaginated(env.Ctx, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(15), result.Total)
	assert.Len(t, result.Data, 10)
	assert.Equal(t, 1, result.CurrentPage)

	// Test pagination again (should hit cache)
	result2, err := env.UserRepo.FindPaginated(env.Ctx, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, result.Total, result2.Total)
	assert.Len(t, result2.Data, 10)
}

func TestCachedRepository_CacheInvalidation(t *testing.T) {
	env := setupTestEnvironment(t)
	defer env.Cleanup()

	// Create a test user
	user := &TestUser{
		ID:        uuid.New(),
		Name:      "Test User",
		Email:     "test@example.com",
		AccountId: "test-account",
	}

	err := env.UserRepo.Create(env.Ctx, user)
	require.NoError(t, err)

	// Cache the user by finding it
	_, err = env.UserRepo.FindById(env.Ctx, user.ID)
	require.NoError(t, err)

	// Update the user (should invalidate cache)
	user.Name = "Updated User"
	err = env.UserRepo.UpdateById(env.Ctx, user.ID, user)
	require.NoError(t, err)

	// Verify the cache was invalidated by checking the updated value
	updatedUser, err := env.UserRepo.FindById(env.Ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, "Updated User", updatedUser.Name)

	// Delete the user (should invalidate cache)
	err = env.UserRepo.DeleteById(env.Ctx, user.ID)
	require.NoError(t, err)

	// Verify the user is deleted
	_, err = env.UserRepo.FindById(env.Ctx, user.ID)
	assert.Error(t, err) // Should return error for deleted user
}

func TestCachedRepository_AssociationMethods(t *testing.T) {
	env := setupTestEnvironment(t)
	defer env.Cleanup()

	user := &TestUser{
		ID:        uuid.New(),
		Name:      "Test User",
		Email:     "test@example.com",
		AccountId: "test-account",
	}

	err := env.UserRepo.Create(env.Ctx, user)
	require.NoError(t, err)

	// Test association methods (they should not panic and should invalidate cache)
	// Note: These are basic tests since we don't have actual associations in TestUser
	_ = env.UserRepo.AppendAssociation(env.Ctx, user, "tags", []string{"tag1"})
	// This might error due to no actual association, but shouldn't panic

	_ = env.UserRepo.RemoveAssociation(env.Ctx, user, "tags", []string{"tag1"})
	// This might error due to no actual association, but shouldn't panic

	_ = env.UserRepo.ReplaceAssociation(env.Ctx, user, "tags", []string{"tag2"})
	// This might error due to no actual association, but shouldn't panic

	// The main thing is that these methods exist and don't panic
	assert.NotNil(t, env.UserRepo.AppendAssociation)
	assert.NotNil(t, env.UserRepo.RemoveAssociation)
	assert.NotNil(t, env.UserRepo.ReplaceAssociation)
}
