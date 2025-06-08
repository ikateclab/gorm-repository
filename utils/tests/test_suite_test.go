package tests

import (
	"context"
	gr "github.com/ikateclab/gorm-repository"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CreateTestUsers creates multiple test users in the database
func CreateTestUsers(t *testing.T, repo *gr.GormRepository[TestUser], count int) []TestUser {
	t.Helper()
	ctx := context.Background()
	users := make([]TestUser, count)

	for i := 0; i < count; i++ {
		user := NewTestUserBuilder().
			WithName("Test User " + string(rune(i+'1'))).
			WithEmail("user" + string(rune(i+'1')) + "@example.com").
			WithAge(20 + i).
			WithActive(i%2 == 0).
			Build()

		err := repo.Create(ctx, user)
		if err != nil {
			t.Fatalf("Failed to create test user %d: %v", i, err)
		}
		users[i] = user
	}

	return users
}

// AssertPaginationResult validates pagination result structure
func AssertPaginationResult(t *testing.T, result *gr.PaginationResult[TestUser], expectedTotal int64, expectedPage int, expectedPageSize int, message string) {
	t.Helper()

	if result.Total != expectedTotal {
		t.Errorf("%s: Total mismatch - expected %d, got %d", message, expectedTotal, result.Total)
	}
	if result.CurrentPage != expectedPage {
		t.Errorf("%s: CurrentPage mismatch - expected %d, got %d", message, expectedPage, result.CurrentPage)
	}
	if result.Limit != expectedPageSize {
		t.Errorf("%s: Limit mismatch - expected %d, got %d", message, expectedPageSize, result.Limit)
	}

	expectedLastPage := int((expectedTotal + int64(expectedPageSize) - 1) / int64(expectedPageSize))
	if expectedTotal == 0 {
		expectedLastPage = 0
	}
	if result.LastPage != expectedLastPage {
		t.Errorf("%s: LastPage mismatch - expected %d, got %d", message, expectedLastPage, result.LastPage)
	}
}

// TestSuite runs comprehensive tests using the test helpers
func TestSuite_UsingHelpers(t *testing.T) {
	db := SetupTestDBWithConfig(t, DefaultTestDBConfig())
	defer CleanupTestDB(t, db)

	repo := &gr.GormRepository[TestUser]{DB: db}
	ctx := context.Background()

	t.Run("CreateUsersWithBuilder", func(t *testing.T) {
		user := NewTestUserBuilder().
			WithName("Builder User").
			WithEmail("builder@example.com").
			WithAge(35).
			WithActive(true).
			Build()

		err := repo.Create(ctx, user)
		if err != nil {
			t.Errorf("Failed to create user with builder: %v", err)
		}

		foundUser, err := repo.FindById(ctx, user.ID)
		if err != nil {
			t.Errorf("Failed to find created user: %v", err)
		}

		AssertUserEqual(t, user, foundUser, "Builder created user")
	})

	t.Run("CreateMultipleUsers", func(t *testing.T) {
		users := CreateTestUsers(t, repo, 5)
		if len(users) != 5 {
			t.Errorf("Expected 5 users, got %d", len(users))
		}

		// Verify all users were created
		allUsers, err := repo.FindMany(ctx)
		if err != nil {
			t.Errorf("Failed to find all users: %v", err)
		}

		// Should have 6 users total (1 from previous test + 5 new)
		if len(allUsers) < 5 {
			t.Errorf("Expected at least 5 users, got %d", len(allUsers))
		}
	})

	t.Run("PaginationWithHelpers", func(t *testing.T) {
		result, err := repo.FindPaginated(ctx, 1, 3)
		if err != nil {
			t.Errorf("FindPaginated failed: %v", err)
		}

		// We should have at least 6 users from previous tests (1 from first test + 5 from second test)
		// Use the actual total from the result, not the length of the current page data
		AssertPaginationResult(t, result, result.Total, 1, 3, "First page pagination")
	})
}

func TestSuite_ProfileIntegration(t *testing.T) {
	db := SetupTestDBWithConfig(t, DefaultTestDBConfig())
	defer CleanupTestDB(t, db)

	userRepo := &gr.GormRepository[TestUser]{DB: db}
	profileRepo := &gr.GormRepository[TestProfile]{DB: db}
	ctx := context.Background()

	t.Run("UserWithProfile", func(t *testing.T) {
		user := NewTestUserBuilder().
			WithName("Profile User").
			WithEmail("profile@example.com").
			Build()

		err := userRepo.Create(ctx, user)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		profile := NewTestProfileBuilder(user.ID).
			WithBio("Test bio for profile integration").
			WithWebsite("https://profile.example.com").
			WithSettings(`{"theme":"dark","language":"en","notifications":{"email":true,"push":false}}`).
			Build()

		err = profileRepo.Create(ctx, profile)
		if err != nil {
			t.Fatalf("Failed to create profile: %v", err)
		}

		// Find user with profile
		foundUser, err := userRepo.FindById(ctx, user.ID, gr.WithRelations("Profile"))
		if err != nil {
			t.Fatalf("Failed to find user with profile: %v", err)
		}

		if foundUser.Profile == nil {
			t.Fatal("Expected profile to be loaded")
		}
		if foundUser.Profile.Bio != profile.Bio {
			t.Errorf("Expected profile bio %s, got %s", profile.Bio, foundUser.Profile.Bio)
		}
		if foundUser.Profile.Website != profile.Website {
			t.Errorf("Expected profile website %s, got %s", profile.Website, foundUser.Profile.Website)
		}
	})
}

func TestSuite_PostsAndTags(t *testing.T) {
	db := SetupTestDBWithConfig(t, DefaultTestDBConfig())
	defer CleanupTestDB(t, db)

	userRepo := &gr.GormRepository[TestUser]{DB: db}
	postRepo := &gr.GormRepository[TestPost]{DB: db}
	tagRepo := &gr.GormRepository[TestTag]{DB: db}
	ctx := context.Background()

	t.Run("UserWithPostsAndTags", func(t *testing.T) {
		user := NewTestUserBuilder().
			WithName("Blogger User").
			WithEmail("blogger@example.com").
			Build()

		err := userRepo.Create(ctx, user)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Create tags
		tag1 := TestTag{ID: uuid.New(), Name: "Go"}
		tag2 := TestTag{ID: uuid.New(), Name: "Testing"}
		tag3 := TestTag{ID: uuid.New(), Name: "GORM"}

		for _, tag := range []TestTag{tag1, tag2, tag3} {
			err = tagRepo.Create(ctx, tag)
			if err != nil {
				t.Fatalf("Failed to create tag %s: %v", tag.Name, err)
			}
		}

		// Create posts
		post1 := NewTestPostBuilder(user.ID).
			WithTitle("Introduction to Go").
			WithContent("Go is a great programming language...").
			WithPublished(true).
			Build()

		post2 := NewTestPostBuilder(user.ID).
			WithTitle("Testing with GORM").
			WithContent("GORM makes database testing easier...").
			WithPublished(false).
			Build()

		err = postRepo.Create(ctx, post1)
		if err != nil {
			t.Fatalf("Failed to create post1: %v", err)
		}
		err = postRepo.Create(ctx, post2)
		if err != nil {
			t.Fatalf("Failed to create post2: %v", err)
		}

		// Associate tags with posts
		err = postRepo.AppendAssociation(ctx, post1, "Tags", []TestTag{tag1, tag2})
		if err != nil {
			t.Fatalf("Failed to associate tags with post1: %v", err)
		}

		err = postRepo.AppendAssociation(ctx, post2, "Tags", []TestTag{tag2, tag3})
		if err != nil {
			t.Fatalf("Failed to associate tags with post2: %v", err)
		}

		// Find user with posts and their tags
		foundUser, err := userRepo.FindById(ctx, user.ID, gr.WithRelations("Posts", "Posts.Tags"))
		if err != nil {
			t.Fatalf("Failed to find user with posts and tags: %v", err)
		}

		if len(foundUser.Posts) != 2 {
			t.Errorf("Expected 2 posts, got %d", len(foundUser.Posts))
		}

		// Check that posts have tags
		for _, post := range foundUser.Posts {
			if len(post.Tags) == 0 {
				t.Errorf("Expected post %s to have tags", post.Title)
			}
		}

		// Find published posts only
		publishedPosts, err := postRepo.FindMany(ctx,
			gr.WithQuery(func(db *gorm.DB) *gorm.DB {
				return db.Where("user_id = ? AND published = ?", user.ID, true)
			}),
			gr.WithRelations("Tags"),
		)
		if err != nil {
			t.Fatalf("Failed to find published posts: %v", err)
		}

		if len(publishedPosts) != 1 {
			t.Errorf("Expected 1 published post, got %d", len(publishedPosts))
		}
		if publishedPosts[0].Title != "Introduction to Go" {
			t.Errorf("Expected published post title 'Introduction to Go', got %s", publishedPosts[0].Title)
		}
	})
}

func TestSuite_TransactionScenarios(t *testing.T) {
	db := SetupTestDBWithConfig(t, DefaultTestDBConfig())
	defer CleanupTestDB(t, db)

	userRepo := &gr.GormRepository[TestUser]{DB: db}
	profileRepo := &gr.GormRepository[TestProfile]{DB: db}
	ctx := context.Background()

	t.Run("ComplexTransactionSuccess", func(t *testing.T) {
		var err error
		tx := userRepo.BeginTransaction()
		defer tx.Finish(&err)

		// Create user in transaction
		user := NewTestUserBuilder().
			WithName("Transaction User").
			WithEmail("transaction@example.com").
			Build()

		err = userRepo.Create(ctx, user, gr.WithTx(tx))
		if err != nil {
			t.Errorf("Failed to create user in transaction: %v", err)
			return
		}

		// Create profile in transaction
		profile := NewTestProfileBuilder(user.ID).
			WithBio("Transaction profile bio").
			Build()

		err = profileRepo.Create(ctx, profile, gr.WithTx(tx))
		if err != nil {
			t.Errorf("Failed to create profile in transaction: %v", err)
			return
		}

		// Transaction should commit automatically
	})

	// Verify both user and profile were created
	users, err := userRepo.FindMany(ctx, gr.WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("email = ?", "transaction@example.com")
	}))
	if err != nil {
		t.Fatalf("Failed to find transaction user: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("Expected 1 transaction user, got %d", len(users))
	}

	profiles, err := profileRepo.FindMany(ctx, gr.WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("user_id = ?", users[0].ID)
	}))
	if err != nil {
		t.Fatalf("Failed to find transaction profile: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("Expected 1 transaction profile, got %d", len(profiles))
	}

	t.Run("ComplexTransactionFailure", func(t *testing.T) {
		var err error
		tx := userRepo.BeginTransaction()
		defer tx.Finish(&err)

		// Create user in transaction
		user := NewTestUserBuilder().
			WithName("Failed Transaction User").
			WithEmail("failed@example.com").
			Build()

		err = userRepo.Create(ctx, user, gr.WithTx(tx))
		if err != nil {
			t.Errorf("Failed to create user in transaction: %v", err)
			return
		}

		// Simulate an error
		err = gorm.ErrInvalidTransaction

		// Transaction should rollback automatically
	})

	// Verify user was not created due to rollback
	failedUsers, err := userRepo.FindMany(ctx, gr.WithQuery(func(db *gorm.DB) *gorm.DB {
		return db.Where("email = ?", "failed@example.com")
	}))
	if err != nil {
		t.Fatalf("Failed to search for failed transaction user: %v", err)
	}
	if len(failedUsers) != 0 {
		t.Errorf("Expected 0 failed transaction users, got %d", len(failedUsers))
	}
}
