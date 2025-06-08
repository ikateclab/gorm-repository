package tests

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestDBConfig holds configuration for test databases
type TestDBConfig struct {
	LogLevel logger.LogLevel
	DSN      string
}

// DefaultTestDBConfig returns a default configuration for test databases
func DefaultTestDBConfig() TestDBConfig {
	return TestDBConfig{
		LogLevel: logger.Silent,
		DSN:      ":memory:",
	}
}

// SetupTestDBWithConfig creates a test database with custom configuration
func SetupTestDBWithConfig(t *testing.T, config TestDBConfig) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(config.DSN), &gorm.Config{
		Logger: logger.Default.LogMode(config.LogLevel),
	})
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Auto-migrate test models
	err = db.AutoMigrate(&TestUser{}, &TestProfile{}, &TestPost{}, &TestTag{}, &TestSimpleEntity{})
	if err != nil {
		t.Fatalf("Failed to migrate test models: %v", err)
	}

	return db
}

// TestUserBuilder provides a fluent interface for creating test users
type TestUserBuilder struct {
	user TestUser
}

// NewTestUserBuilder creates a new test user builder with default values
func NewTestUserBuilder() *TestUserBuilder {
	return &TestUserBuilder{
		user: TestUser{
			ID:     uuid.New(),
			Name:   "Test User",
			Email:  "test@example.com",
			Age:    25,
			Active: true,
		},
	}
}

// WithID sets the user ID
func (b *TestUserBuilder) WithID(id uuid.UUID) *TestUserBuilder {
	b.user.ID = id
	return b
}

// WithName sets the user name
func (b *TestUserBuilder) WithName(name string) *TestUserBuilder {
	b.user.Name = name
	return b
}

// WithEmail sets the user email
func (b *TestUserBuilder) WithEmail(email string) *TestUserBuilder {
	b.user.Email = email
	return b
}

// WithAge sets the user age
func (b *TestUserBuilder) WithAge(age int) *TestUserBuilder {
	b.user.Age = age
	return b
}

// WithActive sets the user active status
func (b *TestUserBuilder) WithActive(active bool) *TestUserBuilder {
	b.user.Active = active
	return b
}

// WithProfile sets the user profile
func (b *TestUserBuilder) WithProfile(profile *TestProfile) *TestUserBuilder {
	b.user.Profile = profile
	return b
}

// WithPosts sets the user posts
func (b *TestUserBuilder) WithPosts(posts []TestPost) *TestUserBuilder {
	b.user.Posts = posts
	return b
}

// Build returns the constructed test user
func (b *TestUserBuilder) Build() TestUser {
	return b.user
}

// TestProfileBuilder provides a fluent interface for creating test profiles
type TestProfileBuilder struct {
	profile TestProfile
}

// NewTestProfileBuilder creates a new test profile builder with default values
func NewTestProfileBuilder(userID uuid.UUID) *TestProfileBuilder {
	return &TestProfileBuilder{
		profile: TestProfile{
			ID:       uuid.New(),
			UserID:   userID,
			Bio:      "Test bio",
			Website:  "https://example.com",
			Settings: "{}",
		},
	}
}

// WithID sets the profile ID
func (b *TestProfileBuilder) WithID(id uuid.UUID) *TestProfileBuilder {
	b.profile.ID = id
	return b
}

// WithBio sets the profile bio
func (b *TestProfileBuilder) WithBio(bio string) *TestProfileBuilder {
	b.profile.Bio = bio
	return b
}

// WithWebsite sets the profile website
func (b *TestProfileBuilder) WithWebsite(website string) *TestProfileBuilder {
	b.profile.Website = website
	return b
}

// WithSettings sets the profile settings
func (b *TestProfileBuilder) WithSettings(settings string) *TestProfileBuilder {
	b.profile.Settings = settings
	return b
}

// Build returns the constructed test profile
func (b *TestProfileBuilder) Build() TestProfile {
	return b.profile
}

// TestPostBuilder provides a fluent interface for creating test posts
type TestPostBuilder struct {
	post TestPost
}

// NewTestPostBuilder creates a new test post builder with default values
func NewTestPostBuilder(userID uuid.UUID) *TestPostBuilder {
	return &TestPostBuilder{
		post: TestPost{
			ID:        uuid.New(),
			UserID:    userID,
			Title:     "Test Post",
			Content:   "Test content",
			Published: false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}
}

// WithID sets the post ID
func (b *TestPostBuilder) WithID(id uuid.UUID) *TestPostBuilder {
	b.post.ID = id
	return b
}

// WithTitle sets the post title
func (b *TestPostBuilder) WithTitle(title string) *TestPostBuilder {
	b.post.Title = title
	return b
}

// WithContent sets the post content
func (b *TestPostBuilder) WithContent(content string) *TestPostBuilder {
	b.post.Content = content
	return b
}

// WithPublished sets the post published status
func (b *TestPostBuilder) WithPublished(published bool) *TestPostBuilder {
	b.post.Published = published
	return b
}

// WithTags sets the post tags
func (b *TestPostBuilder) WithTags(tags []TestTag) *TestPostBuilder {
	b.post.Tags = tags
	return b
}

// Build returns the constructed test post
func (b *TestPostBuilder) Build() TestPost {
	return b.post
}

// AssertUserEqual compares two users and reports differences
func AssertUserEqual(t *testing.T, expected, actual TestUser, message string) {
	t.Helper()

	if expected.ID != actual.ID {
		t.Errorf("%s: ID mismatch - expected %v, got %v", message, expected.ID, actual.ID)
	}
	if expected.Name != actual.Name {
		t.Errorf("%s: Name mismatch - expected %s, got %s", message, expected.Name, actual.Name)
	}
	if expected.Email != actual.Email {
		t.Errorf("%s: Email mismatch - expected %s, got %s", message, expected.Email, actual.Email)
	}
	if expected.Age != actual.Age {
		t.Errorf("%s: Age mismatch - expected %d, got %d", message, expected.Age, actual.Age)
	}
	if expected.Active != actual.Active {
		t.Errorf("%s: Active mismatch - expected %t, got %t", message, expected.Active, actual.Active)
	}
}

// CleanupTestDB removes all data from test tables
func CleanupTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Delete in reverse order of dependencies
	db.Exec("DELETE FROM post_tags")
	db.Exec("DELETE FROM test_posts")
	db.Exec("DELETE FROM test_tags")
	db.Exec("DELETE FROM test_profiles")
	db.Exec("DELETE FROM test_users")
	db.Exec("DELETE FROM test_simple_entities")
}
