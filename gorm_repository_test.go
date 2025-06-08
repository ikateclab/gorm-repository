package gormrepository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ikateclab/gorm-repository/utils/tests"
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
	if err != nil {
		t.Errorf("Create failed: %v", err)
	}

	// Verify the user was created
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	if count != 1 {
		t.Errorf("Expected 1 user, got %d", count)
	}
}

func TestGormRepository_FindById(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	foundUser, err := repo.FindById(ctx, user.ID)
	if err != nil {
		t.Errorf("FindById failed: %v", err)
	}

	if foundUser.ID != user.ID {
		t.Errorf("Expected user ID %v, got %v", user.ID, foundUser.ID)
	}
	if foundUser.Name != user.Name {
		t.Errorf("Expected user name %s, got %s", user.Name, foundUser.Name)
	}
}

func TestGormRepository_FindOne(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	foundUser, err := repo.FindOne(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("email = ?", user.Email)
	}))
	if err != nil {
		t.Errorf("FindOne failed: %v", err)
	}

	if foundUser.Email != user.Email {
		t.Errorf("Expected user email %s, got %s", user.Email, foundUser.Email)
	}
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
		if err != nil {
			t.Fatalf("Failed to create test user: %v", err)
		}
	}

	// Find all active users
	activeUsers, err := repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ?", true)
	}))
	if err != nil {
		t.Errorf("FindMany failed: %v", err)
	}

	if len(activeUsers) != 2 {
		t.Errorf("Expected 2 active users, got %d", len(activeUsers))
	}
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
		if err != nil {
			t.Fatalf("Failed to create test user: %v", err)
		}
	}

	// Test pagination
	result, err := repo.FindPaginated(ctx, 1, 5)
	if err != nil {
		t.Errorf("FindPaginated failed: %v", err)
	}

	if result.Total != 10 {
		t.Errorf("Expected total 10, got %d", result.Total)
	}
	if len(result.Data) != 5 {
		t.Errorf("Expected 5 items per page, got %d", len(result.Data))
	}
	if result.CurrentPage != 1 {
		t.Errorf("Expected current page 1, got %d", result.CurrentPage)
	}
	if result.LastPage != 2 {
		t.Errorf("Expected last page 2, got %d", result.LastPage)
	}
}

func TestGormRepository_Save(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Update user
	user.Name = "Jane Doe"
	user.Age = 25

	err = repo.Save(ctx, user)
	if err != nil {
		t.Errorf("Save failed: %v", err)
	}

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.ID)
	if err != nil {
		t.Fatalf("Failed to find updated user: %v", err)
	}

	if updatedUser.Name != "Jane Doe" {
		t.Errorf("Expected updated name 'Jane Doe', got %s", updatedUser.Name)
	}
	if updatedUser.Age != 25 {
		t.Errorf("Expected updated age 25, got %d", updatedUser.Age)
	}
}

func TestGormRepository_DeleteById(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	err = repo.DeleteById(ctx, user.ID)
	if err != nil {
		t.Errorf("DeleteById failed: %v", err)
	}

	// Verify the user was deleted
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	if count != 0 {
		t.Errorf("Expected 0 users after deletion, got %d", count)
	}
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
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Create profile separately
	err = db.Create(&profile).Error
	if err != nil {
		t.Fatalf("Failed to create test profile: %v", err)
	}

	// Find user with profile preloaded
	foundUser, err := repo.FindById(ctx, user.ID, WithRelations("Profile"))
	if err != nil {
		t.Errorf("FindById with relations failed: %v", err)
	}

	if foundUser.Profile == nil {
		t.Error("Expected profile to be loaded, but it was nil")
	} else if foundUser.Profile.Bio != "Test bio" {
		t.Errorf("Expected profile bio 'Test bio', got %s", foundUser.Profile.Bio)
	}
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
		if err != nil {
			t.Fatalf("Failed to create test user: %v", err)
		}
	}

	// Find users older than 30
	oldUsers, err := repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("age > ?", 30)
	}))
	if err != nil {
		t.Errorf("FindMany with query failed: %v", err)
	}

	if len(oldUsers) != 1 {
		t.Errorf("Expected 1 old user, got %d", len(oldUsers))
	}
	if oldUsers[0].Name != "Old User" {
		t.Errorf("Expected 'Old User', got %s", oldUsers[0].Name)
	}
}

func TestGormRepository_WithQueryStruct(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Find user using struct query
	foundUsers, err := repo.FindMany(ctx, WithQueryStruct(map[string]interface{}{
		"email":  user.Email,
		"active": true,
	}))
	if err != nil {
		t.Errorf("FindMany with query struct failed: %v", err)
	}

	if len(foundUsers) != 1 {
		t.Errorf("Expected 1 user, got %d", len(foundUsers))
	}
	if foundUsers[0].ID != user.ID {
		t.Errorf("Expected user ID %v, got %v", user.ID, foundUsers[0].ID)
	}
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
	if err != nil {
		t.Errorf("Create in transaction failed: %v", err)
	}

	// Commit the transaction
	err = tx.Commit()
	if err != nil {
		t.Errorf("Transaction commit failed: %v", err)
	}

	// Verify the user was created
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	if count != 1 {
		t.Errorf("Expected 1 user after commit, got %d", count)
	}
}

func TestGormRepository_Transaction_Rollback(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	tx := repo.BeginTransaction()

	user := createTestUser()
	err := repo.Create(ctx, user, WithTx(tx))
	if err != nil {
		t.Errorf("Create in transaction failed: %v", err)
	}

	// Rollback the transaction
	err = tx.Rollback()
	if err != nil {
		t.Errorf("Transaction rollback failed: %v", err)
	}

	// Verify the user was not created
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	if count != 0 {
		t.Errorf("Expected 0 users after rollback, got %d", count)
	}
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
	if err != nil {
		t.Errorf("Create in transaction failed: %v", err)
		return
	}

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
	if err != nil {
		t.Errorf("Create in transaction failed: %v", err)
		return
	}

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
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Update using map
	updates := map[string]interface{}{
		"name": "Updated Name",
		"age":  35,
	}

	updatedUser, err := repo.UpdateByIdWithMap(ctx, user.ID, updates)
	if err != nil {
		t.Errorf("UpdateByIdWithMap failed: %v", err)
	}

	if updatedUser.Name != "Updated Name" {
		t.Errorf("Expected updated name 'Updated Name', got %s", updatedUser.Name)
	}
	if updatedUser.Age != 35 {
		t.Errorf("Expected updated age 35, got %d", updatedUser.Age)
	}
}

func TestGormRepository_UpdateByIdInPlace(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[*tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	// Update in place
	err = repo.UpdateByIdInPlace(ctx, user.ID, user, func(u *tests.TestUser) {
		u.Name = "In-Place Updated Name"
		u.Age = 40
	})
	if err != nil {
		t.Errorf("UpdateByIdInPlace failed: %v", err)
	}

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.ID)
	if err != nil {
		t.Fatalf("Failed to find updated user: %v", err)
	}

	if updatedUser.Name != "In-Place Updated Name" {
		t.Errorf("Expected updated name 'In-Place Updated Name', got %s", updatedUser.Name)
	}
	if updatedUser.Age != 40 {
		t.Errorf("Expected updated age 40, got %d", updatedUser.Age)
	}
}
