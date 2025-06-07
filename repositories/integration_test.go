package repositories

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ikateclab/gorm-repository/utils/tests"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupIntegrationDB creates a more comprehensive test database
func setupIntegrationDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("Failed to connect to integration test database: %v", err)
	}

	// Auto-migrate all test models
	err = db.AutoMigrate(&tests.TestUser{}, &tests.TestProfile{}, &tests.TestPost{}, &tests.TestTag{}, &tests.TestSimpleEntity{})
	if err != nil {
		t.Fatalf("Failed to migrate integration test models: %v", err)
	}

	return db
}

func TestIntegration_CompleteUserWorkflow(t *testing.T) {
	db := setupIntegrationDB(t)
	userRepo := &GormRepository[tests.TestUser]{DB: db}
	profileRepo := &GormRepository[tests.TestProfile]{DB: db}
	ctx := context.Background()

	// Create a user
	user := tests.TestUser{
		ID:     uuid.New(),
		Name:   "Integration Test User",
		Email:  "integration@example.com",
		Age:    28,
		Active: true,
	}

	err := userRepo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create a profile for the user
	profile := tests.TestProfile{
		ID:      uuid.New(),
		UserID:  user.ID,
		Bio:     "Integration test bio",
		Website: "https://integration.example.com",
		Settings: `{"theme":"dark","language":"en"}`,
	}

	err = profileRepo.Create(ctx, profile)
	if err != nil {
		t.Fatalf("Failed to create profile: %v", err)
	}

	// Find user with profile preloaded
	foundUser, err := userRepo.FindById(ctx, user.ID, WithRelations("Profile"))
	if err != nil {
		t.Fatalf("Failed to find user with profile: %v", err)
	}

	// Verify user data
	if foundUser.Name != user.Name {
		t.Errorf("Expected user name %s, got %s", user.Name, foundUser.Name)
	}

	// Verify profile was loaded
	if foundUser.Profile == nil {
		t.Fatal("Expected profile to be loaded, but it was nil")
	}
	if foundUser.Profile.Bio != profile.Bio {
		t.Errorf("Expected profile bio %s, got %s", profile.Bio, foundUser.Profile.Bio)
	}

	// Update user using Save
	foundUser.Age = 30
	foundUser.Name = "Updated Integration User"

	err = userRepo.Save(ctx, foundUser)
	if err != nil {
		t.Fatalf("Failed to save updated user: %v", err)
	}

	// Verify update
	updatedUser, err := userRepo.FindById(ctx, user.ID)
	if err != nil {
		t.Fatalf("Failed to find updated user: %v", err)
	}

	if updatedUser.Age != 30 {
		t.Errorf("Expected updated age 30, got %d", updatedUser.Age)
	}
	if updatedUser.Name != "Updated Integration User" {
		t.Errorf("Expected updated name 'Updated Integration User', got %s", updatedUser.Name)
	}
}

func TestIntegration_TransactionWorkflow(t *testing.T) {
	db := setupIntegrationDB(t)
	userRepo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Test successful transaction
	t.Run("Successful Transaction", func(t *testing.T) {
		var err error
		tx := userRepo.BeginTransaction()
		defer tx.Finish(&err)

		user1 := tests.TestUser{
			ID:     uuid.New(),
			Name:   "Transaction User 1",
			Email:  "tx1@example.com",
			Age:    25,
			Active: true,
		}

		user2 := tests.TestUser{
			ID:     uuid.New(),
			Name:   "Transaction User 2",
			Email:  "tx2@example.com",
			Age:    30,
			Active: true,
		}

		err = userRepo.Create(ctx, user1, WithTx(tx))
		if err != nil {
			t.Errorf("Failed to create user1 in transaction: %v", err)
			return
		}

		err = userRepo.Create(ctx, user2, WithTx(tx))
		if err != nil {
			t.Errorf("Failed to create user2 in transaction: %v", err)
			return
		}

		// Transaction should commit automatically via defer
	})

	// Verify both users were created
	users, err := userRepo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("email IN ?", []string{"tx1@example.com", "tx2@example.com"})
	}))
	if err != nil {
		t.Fatalf("Failed to find transaction users: %v", err)
	}

	if len(users) != 2 {
		t.Errorf("Expected 2 users after successful transaction, got %d", len(users))
	}

	// Test failed transaction
	t.Run("Failed Transaction", func(t *testing.T) {
		var err error
		tx := userRepo.BeginTransaction()
		defer tx.Finish(&err)

		user3 := tests.TestUser{
			ID:     uuid.New(),
			Name:   "Transaction User 3",
			Email:  "tx3@example.com",
			Age:    35,
			Active: true,
		}

		err = userRepo.Create(ctx, user3, WithTx(tx))
		if err != nil {
			t.Errorf("Failed to create user3 in transaction: %v", err)
			return
		}

		// Simulate an error
		err = gorm.ErrInvalidTransaction

		// Transaction should rollback automatically via defer
	})

	// Verify user3 was not created due to rollback
	user3Count := int64(0)
	db.Model(&tests.TestUser{}).Where("email = ?", "tx3@example.com").Count(&user3Count)
	if user3Count != 0 {
		t.Errorf("Expected 0 users with email tx3@example.com after rollback, got %d", user3Count)
	}
}

func TestIntegration_PaginationWithLargeDataset(t *testing.T) {
	db := setupIntegrationDB(t)
	userRepo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Create 25 test users
	for i := 0; i < 25; i++ {
		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   "Pagination User " + string(rune(i+'1')),
			Email:  "pagination" + string(rune(i+'1')) + "@example.com",
			Age:    20 + i,
			Active: i%2 == 0, // Alternate active/inactive
		}
		err := userRepo.Create(ctx, user)
		if err != nil {
			t.Fatalf("Failed to create pagination test user %d: %v", i, err)
		}
	}

	// Test first page
	page1, err := userRepo.FindPaginated(ctx, 1, 10)
	if err != nil {
		t.Fatalf("Failed to get first page: %v", err)
	}

	if page1.Total != 25 {
		t.Errorf("Expected total 25, got %d", page1.Total)
	}
	if len(page1.Data) != 10 {
		t.Errorf("Expected 10 items on first page, got %d", len(page1.Data))
	}
	if page1.CurrentPage != 1 {
		t.Errorf("Expected current page 1, got %d", page1.CurrentPage)
	}
	if page1.LastPage != 3 {
		t.Errorf("Expected last page 3, got %d", page1.LastPage)
	}

	// Test last page
	page3, err := userRepo.FindPaginated(ctx, 3, 10)
	if err != nil {
		t.Fatalf("Failed to get last page: %v", err)
	}

	if len(page3.Data) != 5 {
		t.Errorf("Expected 5 items on last page, got %d", len(page3.Data))
	}
	if page3.CurrentPage != 3 {
		t.Errorf("Expected current page 3, got %d", page3.CurrentPage)
	}

	// Test pagination with filters
	activePage1, err := userRepo.FindPaginated(ctx, 1, 5, WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("active = ?", true)
	}))
	if err != nil {
		t.Fatalf("Failed to get filtered page: %v", err)
	}

	if activePage1.Total != 13 { // 13 active users (0, 2, 4, 6, 8, 10, 12, 14, 16, 18, 20, 22, 24)
		t.Errorf("Expected 13 active users, got %d", activePage1.Total)
	}
	if len(activePage1.Data) != 5 {
		t.Errorf("Expected 5 items on filtered page, got %d", len(activePage1.Data))
	}
}

func TestIntegration_AssociationManagement(t *testing.T) {
	db := setupIntegrationDB(t)
	userRepo := &GormRepository[tests.TestUser]{DB: db}
	postRepo := &GormRepository[tests.TestPost]{DB: db}
	tagRepo := &GormRepository[tests.TestTag]{DB: db}
	ctx := context.Background()

	// Create user
	user := tests.TestUser{
		ID:     uuid.New(),
		Name:   "Association Test User",
		Email:  "associations@example.com",
		Age:    30,
		Active: true,
	}
	err := userRepo.Create(ctx, user)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create tags
	tag1 := tests.TestTag{ID: uuid.New(), Name: "Go"}
	tag2 := tests.TestTag{ID: uuid.New(), Name: "Testing"}

	err = tagRepo.Create(ctx, tag1)
	if err != nil {
		t.Fatalf("Failed to create tag1: %v", err)
	}
	err = tagRepo.Create(ctx, tag2)
	if err != nil {
		t.Fatalf("Failed to create tag2: %v", err)
	}

	// Create post
	post := tests.TestPost{
		ID:        uuid.New(),
		UserID:    user.ID,
		Title:     "Test Post",
		Content:   "This is a test post content",
		Published: true,
	}
	err = postRepo.Create(ctx, post)
	if err != nil {
		t.Fatalf("Failed to create post: %v", err)
	}

	// Test association append
	err = postRepo.AppendAssociation(ctx, post, "Tags", []tests.TestTag{tag1, tag2})
	if err != nil {
		t.Fatalf("Failed to append tags to post: %v", err)
	}

	// Verify associations were created
	foundPost, err := postRepo.FindById(ctx, post.ID, WithRelations("Tags"))
	if err != nil {
		t.Fatalf("Failed to find post with tags: %v", err)
	}

	if len(foundPost.Tags) != 2 {
		t.Errorf("Expected 2 tags on post, got %d", len(foundPost.Tags))
	}
}
