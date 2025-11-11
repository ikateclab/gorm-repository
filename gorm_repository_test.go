package gormrepository

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ikateclab/gorm-repository/utils/tests"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var testDB *gorm.DB

func truncateAllTables(db *gorm.DB) error {
	tables := []string{
		"test_users",
		"test_profiles",
		"test_posts",
		"test_tags",
		"test_simple_entities",
	}
	for _, table := range tables {
		if err := db.Exec(fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", table)).Error; err != nil {
			return fmt.Errorf("failed to truncate %s: %w", table, err)
		}
	}
	return nil
}

func setupTestDB(t *testing.T) *gorm.DB {
	err := truncateAllTables(testDB)
	require.NoError(t, err, "failed to truncate tables before test")
	return testDB
}

// createTestUser creates a test user for testing
func createTestUser() *tests.TestUser {
	now := time.Now()

	return &tests.TestUser{
		Id:         uuid.New(),
		Name:       "John Doe",
		Email:      "john@example.com",
		Age:        30,
		Active:     true,
		ArchivedAt: &now,
		Data: &tests.UserData{
			Day:      10,
			Nickname: "John",
			Married:  true,
		},
	}
}

func TestGormRepository_Create(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
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
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	foundUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "FindById should not fail")

	require.Equal(t, user.Id, foundUser.Id, "User Id should match")
	require.Equal(t, user.Name, foundUser.Name, "User name should match")
}

func TestGormRepository_FindOne(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
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
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create multiple users
	users := []*tests.TestUser{
		{Id: uuid.New(), Name: "User 1", Email: "user1@example.com", Age: 25, Active: true},
		{Id: uuid.New(), Name: "User 2", Email: "user2@example.com", Age: 30, Active: true},
		{Id: uuid.New(), Name: "User 3", Email: "user3@example.com", Age: 35, Active: false},
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
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create 10 test users
	for i := 0; i < 10; i++ {
		user := &tests.TestUser{
			Id:     uuid.New(),
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

func TestGormRepository_Max(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create users with different ages
	users := []*tests.TestUser{
		{Id: uuid.New(), Name: "User 1", Email: "user1@example.com", Age: 25, Active: true},
		{Id: uuid.New(), Name: "User 2", Email: "user2@example.com", Age: 30, Active: true},
		{Id: uuid.New(), Name: "User 3", Email: "user3@example.com", Age: 45, Active: false},
		{Id: uuid.New(), Name: "User 4", Email: "user4@example.com", Age: 20, Active: false},
	}

	for _, user := range users {
		err := repo.Create(ctx, user)
		require.NoError(t, err, "Failed to create test user")
	}

	// Test max age
	maxAge, err := repo.Max(ctx, "age")
	require.NoError(t, err, "Max should not fail")
	require.Equal(t, 45, maxAge, "Expected max age 45")

	// Test max age with WHERE condition
	maxAge, err = repo.Max(ctx, "age", WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ?", true)
	}))
	require.NoError(t, err, "Max with WHERE should not fail")
	require.Equal(t, 30, maxAge, "Expected max age 30 for active users")

	// Test max age with WHERE condition
	maxAge, err = repo.Max(ctx, "age", WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ?", false).Where("age < ?", 40)
	}))
	require.NoError(t, err, "Max with WHERE should not fail")
	require.Equal(t, 20, maxAge, "Expected max age 20 for disabled users with age < 40")
}

func TestGormRepository_MaxEmptyTable(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Test max on empty table
	maxAge, err := repo.Max(ctx, "age")
	require.NoError(t, err, "Max on empty table should not fail")
	require.Equal(t, 0, maxAge, "Expected max 0 on empty table")
}

func TestGormRepository_MaxInvalidColumn(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create a user
	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Test max with invalid column
	_, err = repo.Max(ctx, "invalid_column")
	require.Error(t, err, "Max with invalid column should fail")
}

func TestGormRepository_Save(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
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
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "Jane Doe", updatedUser.Name, "Expected updated name 'Jane Doe'")
	require.Equal(t, 25, updatedUser.Age, "Expected updated age 25")
}

func TestGormRepository_BulkUpdate(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create multiple users
	users := []*tests.TestUser{
		{Id: uuid.New(), Name: "User 1", Email: "user1@example.com", Age: 25, Active: true, Data: &tests.UserData{Married: false}},
		{Id: uuid.New(), Name: "User 2", Email: "user2@example.com", Age: 30, Active: true, Data: nil},
		{Id: uuid.New(), Name: "User 3", Email: "user3@example.com", Age: 35, Active: false, Data: &tests.UserData{Married: true}},
	}

	for _, user := range users {
		err := repo.Create(ctx, user)
		require.NoError(t, err, "Failed to create test user")
	}

	// Find user by name and age - before update
	users, err := repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("name = ?", "User").Where("age = ?", 35)
	}))
	require.NoError(t, err, "FindMany should not fail")
	require.Len(t, users, 0, "Expected 0 users")

	// Update name and age from user
	err = repo.BulkUpdate(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("name <> ?", "User")
	}), map[string]interface{}{"Name": "User", "Age": 35})
	require.NoError(t, err, "BulkUpdate should not fail")

	// Find user by name and age - after update
	users, err = repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("name = ?", "User").Where("age = ?", 35)
	}))
	require.NoError(t, err, "FindMany should not fail")
	require.Len(t, users, 3, "Expected 3 users")

	// Find user by active and married data - before update
	users, err = repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ? OR (data->>'married')::boolean = ?", true, true)
	}))
	require.NoError(t, err, "FindMany should not fail")
	require.Len(t, users, 3, "Expected 3 users")

	// Update active and married data from user
	err = repo.BulkUpdate(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ? OR (data->>'married')::boolean = ?", true, true)
	}), map[string]interface{}{"Active": false, "Data": map[string]interface{}{"Married": false}})
	require.NoError(t, err, "BulkUpdate should not fail")

	// Find user by active and married data - after update
	users, err = repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ? OR (data->>'married')::boolean = ?", true, true)
	}))
	require.NoError(t, err, "FindMany should not fail")
	require.Len(t, users, 0, "Expected 0 users")
}

func TestGormRepository_BulkUpdateInvalidWhere(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	err := repo.BulkUpdate(ctx, nil, map[string]interface{}{})
	require.EqualError(t, err, "WHERE conditions are required for bulk update", "BulkUpdate should fail with nil where")
}

func TestGormRepository_BulkUpdateInvalidJsonMarshal(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	err := repo.BulkUpdate(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("name <> ?", "User")
	}), map[string]interface{}{"InvalidField": "InvalidValue"})
	require.Error(t, err, "BulkUpdate should fail with invalid json marshal")
}

func TestGormRepository_DeleteById(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	err = repo.DeleteById(ctx, user.Id)
	require.NoError(t, err, "DeleteById should not fail")

	// Verify the user was deleted
	var count int64
	db.Model(&tests.TestUser{}).Count(&count)
	require.Equal(t, int64(0), count, "Expected 0 users after deletion")
}

func TestGormRepository_WithRelations(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create user with profile
	user := createTestUser()
	profile := tests.TestProfile{
		Id:      uuid.New(),
		UserId:  user.Id,
		Bio:     "Test bio",
		Website: "https://example.com",
	}

	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Create profile separately
	err = db.Create(&profile).Error
	require.NoError(t, err, "Failed to create test profile")

	// Find user with profile preloaded
	foundUser, err := repo.FindById(ctx, user.Id, WithRelations("Profile"))
	require.NoError(t, err, "FindById with relations should not fail")

	require.NotNil(t, foundUser.Profile, "Expected profile to be loaded")
	require.Equal(t, "Test bio", foundUser.Profile.Bio, "Expected profile bio 'Test bio'")
}

func TestGormRepository_WithQuery(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create users with different ages
	users := []*tests.TestUser{
		{Id: uuid.New(), Name: "Young User", Email: "young@example.com", Age: 20, Active: true},
		{Id: uuid.New(), Name: "Old User", Email: "old@example.com", Age: 50, Active: true},
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
	repo := &GormRepository[tests.TestUser]{DB: db}
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
	require.Equal(t, user.Id, foundUsers[0].Id, "Expected user Id to match")
}

func TestGormRepository_Transaction_Commit(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
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
	repo := &GormRepository[tests.TestUser]{DB: db}
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
	repo := &GormRepository[tests.TestUser]{DB: db}
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
	repo := &GormRepository[tests.TestUser]{DB: db}
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

func TestGormRepository_UpdateById_WithoutTransaction(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Modify the user
	user.Name = "Updated Without Transaction"
	user.Age = 35
	user.Active = false
	user.Data.Day = 20
	user.Data.Nickname = "Doe"
	user.Data.Married = false

	// Update without transaction - should work with blank clone
	err = repo.UpdateById(ctx, user.Id, user)
	require.NoError(t, err, "UpdateById without transaction should not fail")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "Updated Without Transaction", updatedUser.Name, "Expected updated name")
	require.Equal(t, 35, updatedUser.Age, "Expected updated age")

	// Isso é esperado pois não estamos utilizando transaction, o diff só funciona com transaction, caso contrário é passado a entidade.
	// Por padrão o gorm ignora o valor false para tipo booleano, não atualizando o campo.
	require.True(t, updatedUser.Active, "Expected no update active")

	require.Equal(t, "Doe", updatedUser.Data.Nickname, "Expected updated nickname")
	require.Equal(t, 20, updatedUser.Data.Day, "Expected updated day")

	// Isso é esperado pois não estamos utilizando transaction, o diff só funciona com transaction, caso contrário é passado a entidade.
	// Por padrão o gorm ignora o valor false para tipo booleano, não atualizando o campo.
	require.True(t, updatedUser.Data.Married, "Expected no update married")
}

func TestGormRepository_UpdateById_WithTransactionAndClone(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Start transaction and find user (this creates a clone)
	tx := repo.BeginTransaction()

	foundUser, err := repo.FindById(ctx, user.Id, WithTx(tx))
	require.NoError(t, err, "Failed to find user in transaction")

	// Modify the user
	foundUser.Name = "Updated With Transaction"
	foundUser.Age = 40
	foundUser.Active = false
	foundUser.ArchivedAt = nil
	foundUser.Data.Day = 20
	foundUser.Data.Nickname = "Doe"
	foundUser.Data.Married = false

	// Update with transaction and existing clone
	err = repo.UpdateById(ctx, user.Id, foundUser, WithTx(tx))
	require.NoError(t, err, "UpdateById with transaction should not fail")

	// Commit the transaction
	err = tx.Commit()
	require.NoError(t, err, "Failed to commit transaction")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "Updated With Transaction", updatedUser.Name, "Expected updated name")
	require.Equal(t, 40, updatedUser.Age, "Expected updated age")
	require.False(t, updatedUser.Active, "Expected updated active")
	require.Equal(t, 20, updatedUser.Data.Day, "Expected updated day")
	require.Equal(t, "Doe", updatedUser.Data.Nickname, "Expected updated nickname")
	require.False(t, updatedUser.Data.Married, "Expected updated Data.Married")
	require.Nil(t, updatedUser.ArchivedAt, "Expected ArchivedAt to be nil")
}

func TestGormRepository_UpdateById_ZeroValue_WithTransaction(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Start transaction and find user (this creates a clone)
	tx := repo.BeginTransaction()

	foundUser, err := repo.FindById(ctx, user.Id, WithTx(tx))
	require.NoError(t, err, "Failed to find user in transaction")

	// Modify the user
	foundUser.Name = ""
	foundUser.Age = 0
	foundUser.Active = false
	foundUser.ArchivedAt = nil
	foundUser.Data.Day = 0
	foundUser.Data.Nickname = ""
	foundUser.Data.Married = false

	// Update with transaction and existing clone
	err = repo.UpdateById(ctx, user.Id, foundUser, WithTx(tx))
	require.NoError(t, err, "UpdateById with transaction should not fail")

	// Commit the transaction
	err = tx.Commit()
	require.NoError(t, err, "Failed to commit transaction")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Nil(t, updatedUser.ArchivedAt, "Expected ArchivedAt to be nil")
	require.Equal(t, "", updatedUser.Name, "Expected updated name")
	require.Equal(t, 0, updatedUser.Age, "Expected updated age")
	require.False(t, updatedUser.Active, "Expected updated active")
	require.Equal(t, 0, updatedUser.Data.Day, "Expected updated day")
	require.Equal(t, "", updatedUser.Data.Nickname, "Expected updated nickname")
	require.False(t, updatedUser.Data.Married, "Expected updated Data.Married")
}

func TestGormRepository_UpdateById_WithTransactionNoClone(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Start transaction but don't find user (no clone created)
	tx := repo.BeginTransaction()

	// Modify the user
	user.Name = "Updated With Transaction No Clone"
	user.Age = 45

	// Update with transaction but no existing clone - should work with blank clone
	err = repo.UpdateById(ctx, user.Id, user, WithTx(tx))
	require.NoError(t, err, "UpdateById with transaction but no clone should not fail")

	// Commit the transaction
	err = tx.Commit()
	require.NoError(t, err, "Failed to commit transaction")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "Updated With Transaction No Clone", updatedUser.Name, "Expected updated name")
	require.Equal(t, 45, updatedUser.Age, "Expected updated age")
}

func TestGormRepository_UpdateById_NoChanges(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	originalName := user.Name
	originalAge := user.Age
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Don't modify the user - should result in no changes
	err = repo.UpdateById(ctx, user.Id, user)
	require.NoError(t, err, "UpdateById with no changes should not fail")

	// Verify no changes were made
	unchangedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find user")

	require.Equal(t, originalName, unchangedUser.Name, "Name should remain unchanged")
	require.Equal(t, originalAge, unchangedUser.Age, "Age should remain unchanged")
}

func TestGormRepository_UpdateById_NonDiffableEntity(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// Create a repository for a type that doesn't implement Diffable
	type NonDiffableEntity struct {
		Id   uuid.UUID `gorm:"type:text;primary_key"`
		Name string
	}

	repo := &GormRepository[NonDiffableEntity]{DB: db}

	entity := &NonDiffableEntity{
		Id:   uuid.New(),
		Name: "Test",
	}

	// This should fail because NonDiffableEntity doesn't implement Diffable
	err := repo.UpdateById(ctx, entity.Id, entity)
	require.Error(t, err, "UpdateById should fail for non-diffable entity")
	require.Contains(t, err.Error(), "entity must implement Diffable[T] interface", "Error should mention Diffable requirement")
}

func TestGormRepository_UpdateByIdWithMap(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update using map
	updates := map[string]interface{}{
		"name": "Updated Name",
		"age":  35,
	}

	updatedUser, err := repo.UpdateByIdWithMap(ctx, user.Id, updates)
	require.NoError(t, err, "UpdateByIdWithMap should not fail")

	require.Equal(t, "Updated Name", updatedUser.Name, "Expected updated name 'Updated Name'")
	require.Equal(t, 35, updatedUser.Age, "Expected updated age 35")
}

func TestGormRepository_UpdateByIdInPlace(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update in place - the updateFunc takes no parameters and modifies the entity directly
	err = repo.UpdateByIdInPlace(ctx, user.Id, user, func() {
		user.Name = "In-Place Updated Name"
		user.Age = 40
	})
	require.NoError(t, err, "UpdateByIdInPlace should not fail")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "In-Place Updated Name", updatedUser.Name, "Expected updated name 'In-Place Updated Name'")
	require.Equal(t, 40, updatedUser.Age, "Expected updated age 40")
}

func TestGormRepository_UpdateInPlace(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update in place without explicit Id - GORM should extract the primary key from the entity
	err = repo.UpdateInPlace(ctx, user, func() {
		user.Name = "UpdateInPlace Name"
		user.Age = 45
	})
	require.NoError(t, err, "UpdateInPlace should not fail")

	// Verify the update
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "UpdateInPlace Name", updatedUser.Name, "Expected updated name 'UpdateInPlace Name'")
	require.Equal(t, 45, updatedUser.Age, "Expected updated age 45")
}

func TestGormRepository_UpdateInPlace_NoChanges(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update in place with no actual changes
	err = repo.UpdateInPlace(ctx, user, func() {
		// No changes made to the entity
	})
	require.NoError(t, err, "UpdateInPlace with no changes should not fail")

	// Verify no changes were made
	unchangedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find user")

	require.Equal(t, user.Name, unchangedUser.Name, "Name should remain unchanged")
	require.Equal(t, user.Age, unchangedUser.Age, "Age should remain unchanged")
}

func TestGormRepository_UpdateInPlace_NonDiffableEntity(t *testing.T) {
	// This test would require a non-diffable entity type, but since our TestUser implements Diffable,
	// we'll test the error condition by using a different approach
	db := setupTestDB(t)

	// Create a repository for a type that doesn't implement Diffable
	// For this test, we'll use a simple struct that doesn't implement the interface
	type NonDiffableEntity struct {
		Id   uuid.UUID `gorm:"type:text;primary_key"`
		Name string
	}

	repo := &GormRepository[NonDiffableEntity]{DB: db}
	ctx := context.Background()

	entity := NonDiffableEntity{
		Id:   uuid.New(),
		Name: "Test",
	}

	// This should fail because NonDiffableEntity doesn't implement Diffable
	err := repo.UpdateInPlace(ctx, &entity, func() {
		entity.Name = "Updated"
	})
	require.Error(t, err, "UpdateInPlace should fail for non-diffable entity")
	require.Contains(t, err.Error(), "entity does not support diffing", "Error should mention diffing requirement")
}

func TestGormRepository_UpdateInPlace_MultipleFields(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	user := createTestUser()
	originalName := user.Name
	originalAge := user.Age
	originalActive := user.Active

	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Update multiple fields in place
	err = repo.UpdateInPlace(ctx, user, func() {
		user.Name = "Multi-Field Update"
		user.Age = 99
		user.Active = !originalActive
	})
	require.NoError(t, err, "UpdateInPlace with multiple fields should not fail")

	// Verify all updates
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Equal(t, "Multi-Field Update", updatedUser.Name, "Name should be updated")
	require.Equal(t, 99, updatedUser.Age, "Age should be updated")
	require.Equal(t, !originalActive, updatedUser.Active, "Active status should be toggled")
	require.NotEqual(t, originalName, updatedUser.Name, "Name should be different from original")
	require.NotEqual(t, originalAge, updatedUser.Age, "Age should be different from original")
}

// Test UpdateByIdInPlace with transaction focusing on boolean false values
func TestGormRepository_UpdateByIdInPlace_ZeroValue_WithTransaction(t *testing.T) {
	db := setupTestDB(t)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create user with boolean fields set to true
	user := createTestUser()
	user.Active = true
	user.Data.Married = true
	err := repo.Create(ctx, user)
	require.NoError(t, err, "Failed to create test user")

	// Start transaction
	tx := repo.BeginTransaction()

	// Update in place with boolean false values
	err = repo.UpdateByIdInPlace(ctx, user.Id, user, func() {
		user.Name = ""
		user.Active = false
		user.Age = 0
		user.ArchivedAt = nil
		user.Data.Married = false
		user.Data.Day = 0
		user.Data.Nickname = ""
	}, WithTx(tx))
	require.NoError(t, err, "UpdateByIdInPlace with boolean false should not fail")

	// Commit the transaction
	err = tx.Commit()
	require.NoError(t, err, "Failed to commit transaction")

	// Verify the updates
	updatedUser, err := repo.FindById(ctx, user.Id)
	require.NoError(t, err, "Failed to find updated user")

	require.Nil(t, updatedUser.ArchivedAt, "Expected ArchivedAt to be nil")
	require.False(t, updatedUser.Active, "Expected Active to be false")
	require.False(t, updatedUser.Data.Married, "Expected Data.Married to be false")
	require.Equal(t, 0, updatedUser.Data.Day, "Expected Data.Day to be 0")
	require.Equal(t, "", updatedUser.Data.Nickname, "Expected Data.Nickname to be empty")
	require.Equal(t, "", updatedUser.Name, "Expected name to be updated")
	require.Equal(t, 0, updatedUser.Age, "Expected age to be updated")
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Name:         "postgres-test",
		Image:        "postgres:18beta1-alpine3.21",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "postgres",
			"POSTGRES_PASSWORD": "secret",
			"POSTGRES_DB":       "testdb",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithStartupTimeout(30 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
		Reuse:            true,
	})
	if err != nil {
		log.Fatalf("failed to start container: %v", err)
	}

	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5432")

	dsn := fmt.Sprintf("host=%s port=%s user=postgres password=secret dbname=testdb sslmode=disable", host, port.Port())

	// Tenta conectar
	for i := 0; i < 10; i++ {
		testDB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Info),
		})
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		log.Fatalf("failed to connect to DB: %v", err)
	}

	// Migração única
	err = testDB.AutoMigrate(
		&tests.TestUser{},
		&tests.TestProfile{},
		&tests.TestPost{},
		&tests.TestTag{},
		&tests.TestSimpleEntity{},
	)
	if err != nil {
		log.Fatalf("auto-migrate failed: %v", err)
	}

	code := m.Run()

	_ = container.Terminate(ctx)
	os.Exit(code)
}
