package gormrepository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ikateclab/gorm-repository/utils/tests"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupTestDB creates an in-memory SQLite database for testing
func setupTestDB(t *testing.T) *gorm.DB {
	// Use a unique database name for each test to ensure isolation
	dbName := ":memory:"
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Auto-migrate test models
	err = db.AutoMigrate(&tests.TestUser{}, &tests.TestProfile{}, &tests.TestPost{}, &tests.TestTag{}, &tests.TestSimpleEntity{})
	if err != nil {
		t.Fatalf("Failed to migrate test models: %v", err)
	}

	return db
}

// createTestUser creates a test user for testing
func createTestUser() *tests.TestUser {
	return &tests.TestUser{
		ID:     uuid.New(),
		Name:   "John Doe",
		Email:  "john@example.com",
		Age:    30,
		Active: true,
	}
}

func TestGormRepository_Create(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()

	err := repo.Create(ctx, user)
	require.NoError(t, err, "Create should not fail")

	// Verify the user was created
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	require.Equal(t, int64(1), count, "Expected 1 user to be created")
}

func TestGormRepository_FindById(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	foundUser, err := repo.FindById(ctx, user.ID)
	require.NoError(t, err, "FindById should not fail")

	require.Equal(t, user.ID, foundUser.ID, "User ID should match")
	require.Equal(t, user.Name, foundUser.Name, "User name should match")
}

func TestGormRepository_FindOne(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	foundUser, err := repo.FindOne(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("email = ?", user.Email)
	}))
	require.NoError(t, err, "FindOne should not fail")

	require.Equal(t, user.Email, foundUser.Email, "User email should match")
}

func TestGormRepository_FindMany(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create multiple users
	users := []*tests.TestUser{
		{ID: uuid.New(), Name: "User 1", Email: "user1@example.com", Age: 25, Active: true},
		{ID: uuid.New(), Name: "User 2", Email: "user2@example.com", Age: 30, Active: true},
		{ID: uuid.New(), Name: "User 3", Email: "user3@example.com", Age: 35, Active: false},
	}

	for _, user := range users {
		err := repo.Create(ctx, user)
		require.NoError(t, err, "Failed to create test user")
	}

	// Find all active users
	activeUsers, err := repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ?", true)
	}))
	require.NoError(t, err, "FindMany should not fail")

	require.Len(t, activeUsers, 2, "Expected 2 active users")
}

func TestGormRepository_FindPaginated(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create 10 test users
	for i := 0; i < 10; i++ {
		user := &tests.TestUser{
			ID:     uuid.New(),
			Name:   "User " + string(rune(i+'1')),
			Email:  "user" + string(rune(i+'1')) + "@example.com",
			Age:    20 + i,
			Active: true,
		}
		err := repo.Create(ctx, user)
		require.NoError(t, err, "Failed to create test user")
	}

	// Test pagination
	result, err := repo.FindPaginated(ctx, 1, 5)
	require.NoError(t, err, "FindPaginated should not fail")

	require.Equal(t, int64(10), result.Total, "Expected total 10")
	require.Len(t, result.Data, 5, "Expected 5 items per page")
	require.Equal(t, 1, result.CurrentPage, "Expected current page 1")
	require.Equal(t, 2, result.LastPage, "Expected last page 2")
}

func TestGormRepository_Save(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update user
	user.Name = "Jane Doe"
	user.Age = 25

	err = repo.Save(ctx, user)
	require.NoError(t, err, "Save should not fail")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.ID)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "Jane Doe", updatedUser.Name, "Expected updated name 'Jane Doe'")
	require.Equal(t, 25, updatedUser.Age, "Expected updated age 25")
}

func TestGormRepository_DeleteById(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	err = repo.DeleteById(ctx, user.ID)
	require.NoError(t, err, "DeleteById should not fail")

	// Verify the user was deleted
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	require.Equal(t, int64(0), count, "Expected 0 users after deletion")
}

func TestGormRepository_WithRelations(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create user with profile
	user := createTestUser()
	profile := tests.TestProfile{
		ID:      uuid.New(),
		UserID:  user.ID,
		Bio:     "Test bio",
		Website: "https://example.com",
	}

	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Create profile separately
	err = db.Create(&profile).Error
	require.NoError(t, err, "Failed to create test profile")

	// Find user with profile preloaded
	foundUser, err := repo.FindById(ctx, user.ID, WithRelations("Profile"))
	require.NoError(t, err, "FindById with relations should not fail")

	require.NotNil(t, foundUser.Profile, "Expected profile to be loaded")
	require.Equal(t, "Test bio", foundUser.Profile.Bio, "Expected profile bio 'Test bio'")
}

func TestGormRepository_WithQuery(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create users with different ages
	users := []*tests.TestUser{
		{ID: uuid.New(), Name: "Young User", Email: "young@example.com", Age: 20, Active: true},
		{ID: uuid.New(), Name: "Old User", Email: "old@example.com", Age: 50, Active: true},
	}

	for _, user := range users {
		err := repo.Create(ctx, user)
		require.NoError(t, err, "Failed to create test user")
	}

	// Find users older than 30
	oldUsers, err := repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("age > ?", 30)
	}))
	require.NoError(t, err, "FindMany with query should not fail")

	require.Len(t, oldUsers, 1, "Expected 1 old user")
	require.Equal(t, "Old User", oldUsers[0].Name, "Expected 'Old User'")
}

func TestGormRepository_WithQueryStruct(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Find user using struct query
	foundUsers, err := repo.FindMany(ctx, WithQueryStruct(map[string]interface{}{
		"email":  user.Email,
		"active": true,
	}))
	require.NoError(t, err, "FindMany with query struct should not fail")

	require.Len(t, foundUsers, 1, "Expected 1 user")
	require.Equal(t, user.ID, foundUsers[0].ID, "Expected user ID to match")
}

func TestGormRepository_Transaction_Commit(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	tx := repo.BeginTransaction()
	defer func() {
		if !tx.committed && !tx.rolledBack {
			tx.Rollback()
		}
	}()

	user := createTestUser()
	err := repo.Create(ctx, user, WithTx(tx))
	require.NoError(t, err, "Create in transaction should not fail")

	// Commit the transaction
	err = tx.Commit()
	require.NoError(t, err, "Transaction commit should not fail")

	// Verify the user was created
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	require.Equal(t, int64(1), count, "Expected 1 user after commit")
}

func TestGormRepository_Transaction_Rollback(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	tx := repo.BeginTransaction()

	user := createTestUser()
	err := repo.Create(ctx, user, WithTx(tx))
	require.NoError(t, err, "Create in transaction should not fail")

	// Rollback the transaction
	err = tx.Rollback()
	require.NoError(t, err, "Transaction rollback should not fail")

	// Verify the user was not created
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	require.Equal(t, int64(0), count, "Expected 0 users after rollback")
}

func TestGormRepository_Transaction_Finish_Success(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	var err error
	tx := repo.BeginTransaction()
	defer tx.Finish(&err)

	user := createTestUser()
	err = repo.Create(ctx, user, WithTx(tx))
	require.NoError(t, err, "Create in transaction should not fail")

	// err is nil, so transaction should commit
	// Verify after defer executes by checking in a separate test
}

func TestGormRepository_Transaction_Finish_Error(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	var err error
	tx := repo.BeginTransaction()
	defer tx.Finish(&err)

	user := createTestUser()
	err = repo.Create(ctx, user, WithTx(tx))
	require.NoError(t, err, "Create in transaction should not fail")

	// Simulate an error
	err = gorm.ErrInvalidTransaction

	// err is not nil, so transaction should rollback
	// The actual rollback happens in defer
}

func TestGormRepository_UpdateByIdWithMap(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update using map
	updates := map[string]interface{}{
		"name": "Updated Name",
		"age":  35,
	}

	updatedUser, err := repo.UpdateByIdWithMap(ctx, user.ID, updates)
	require.NoError(t, err, "UpdateByIdWithMap should not fail")

	require.Equal(t, "Updated Name", updatedUser.Name, "Expected updated name 'Updated Name'")
	require.Equal(t, 35, updatedUser.Age, "Expected updated age 35")
}

func TestGormRepository_UpdateByIdInPlace(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update in place - the updateFunc takes no parameters and modifies the entity directly
	err = repo.UpdateByIdInPlace(ctx, user.ID, user, func() {
		user.Name = "In-Place Updated Name"
		user.Age = 40
	})
	require.NoError(t, err, "UpdateByIdInPlace should not fail")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.ID)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "In-Place Updated Name", updatedUser.Name, "Expected updated name 'In-Place Updated Name'")
	require.Equal(t, 40, updatedUser.Age, "Expected updated age 40")
}
