package gormrepository

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/ikateclab/gorm-repository/utils"
	"github.com/ikateclab/gorm-repository/utils/tests"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// setupBenchmarkDB creates a database for benchmarking
func setupBenchmarkDB(b *testing.B) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatalf("Failed to connect to benchmark database: %v", err)
	}

	err = db.AutoMigrate(&tests.TestUser{}, &tests.TestProfile{}, &tests.TestPost{}, &tests.TestTag{}, &tests.TestSimpleEntity{})
	if err != nil {
		b.Fatalf("Failed to migrate benchmark models: %v", err)
	}

	return db
}

func BenchmarkGormRepository_Create(b *testing.B) {
	db := setupBenchmarkDB(b)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   fmt.Sprintf("Benchmark User %d", i),
			Email:  fmt.Sprintf("benchmark%d@example.com", i),
			Age:    25,
			Active: true,
		}
		err := repo.Create(ctx, user)
		if err != nil {
			b.Fatalf("Create failed: %v", err)
		}
	}
}

func BenchmarkGormRepository_FindById(b *testing.B) {
	db := setupBenchmarkDB(b)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Pre-create users for benchmarking
	userIDs := make([]uuid.UUID, 1000)
	for i := 0; i < 1000; i++ {
		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   fmt.Sprintf("Benchmark User %d", i),
			Email:  fmt.Sprintf("benchmark%d@example.com", i),
			Age:    25 + i%50,
			Active: true,
		}
		userIDs[i] = user.ID
		err := repo.Create(ctx, user)
		if err != nil {
			b.Fatalf("Failed to create benchmark user: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := repo.FindById(ctx, userIDs[i%1000])
		if err != nil {
			b.Fatalf("FindById failed: %v", err)
		}
	}
}

func BenchmarkGormRepository_FindMany(b *testing.B) {
	db := setupBenchmarkDB(b)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Pre-create users
	for i := 0; i < 100; i++ {
		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   fmt.Sprintf("Benchmark User %d", i),
			Email:  fmt.Sprintf("benchmark%d@example.com", i),
			Age:    25 + i%50,
			Active: i%2 == 0,
		}
		err := repo.Create(ctx, user)
		if err != nil {
			b.Fatalf("Failed to create benchmark user: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := repo.FindMany(ctx, WithQuery(func(db *gorm.DB) *gorm.DB {
			return db.Where("active = ?", true)
		}))
		if err != nil {
			b.Fatalf("FindMany failed: %v", err)
		}
	}
}

func BenchmarkGormRepository_FindPaginated(b *testing.B) {
	db := setupBenchmarkDB(b)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Pre-create users
	for i := 0; i < 1000; i++ {
		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   fmt.Sprintf("Benchmark User %d", i),
			Email:  fmt.Sprintf("benchmark%d@example.com", i),
			Age:    25 + i%50,
			Active: true,
		}
		err := repo.Create(ctx, user)
		if err != nil {
			b.Fatalf("Failed to create benchmark user: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page := (i % 10) + 1 // Cycle through pages 1-10
		_, err := repo.FindPaginated(ctx, page, 50)
		if err != nil {
			b.Fatalf("FindPaginated failed: %v", err)
		}
	}
}

func BenchmarkGormRepository_Save(b *testing.B) {
	db := setupBenchmarkDB(b)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	// Pre-create users
	users := make([]tests.TestUser, 100)
	for i := 0; i < 100; i++ {
		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   fmt.Sprintf("Benchmark User %d", i),
			Email:  fmt.Sprintf("benchmark%d@example.com", i),
			Age:    25,
			Active: true,
		}
		users[i] = user
		err := repo.Create(ctx, user)
		if err != nil {
			b.Fatalf("Failed to create benchmark user: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		user := users[i%100]
		user.Age = 30 + i%20 // Vary the age
		err := repo.Save(ctx, user)
		if err != nil {
			b.Fatalf("Save failed: %v", err)
		}
	}
}

func BenchmarkGormRepository_Transaction(b *testing.B) {
	db := setupBenchmarkDB(b)
	repo := &GormRepository[tests.TestUser]{DB: db}
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		tx := repo.BeginTransaction()

		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   fmt.Sprintf("Transaction User %d", i),
			Email:  fmt.Sprintf("tx%d@example.com", i),
			Age:    25,
			Active: true,
		}

		err = repo.Create(ctx, user, WithTx(tx))
		if err != nil {
			tx.Rollback()
			b.Fatalf("Create in transaction failed: %v", err)
		}

		err = tx.Commit()
		if err != nil {
			b.Fatalf("Transaction commit failed: %v", err)
		}
	}
}

func BenchmarkGormRepository_WithRelations(b *testing.B) {
	db := setupBenchmarkDB(b)
	userRepo := &GormRepository[tests.TestUser]{DB: db}
	profileRepo := &GormRepository[tests.TestProfile]{DB: db}
	ctx := context.Background()

	// Pre-create users with profiles
	userIDs := make([]uuid.UUID, 100)
	for i := 0; i < 100; i++ {
		user := tests.TestUser{
			ID:     uuid.New(),
			Name:   fmt.Sprintf("Benchmark User %d", i),
			Email:  fmt.Sprintf("benchmark%d@example.com", i),
			Age:    25,
			Active: true,
		}
		userIDs[i] = user.ID

		err := userRepo.Create(ctx, user)
		if err != nil {
			b.Fatalf("Failed to create benchmark user: %v", err)
		}

		profile := tests.TestProfile{
			ID:      uuid.New(),
			UserID:  user.ID,
			Bio:     fmt.Sprintf("Benchmark bio %d", i),
			Website: fmt.Sprintf("https://benchmark%d.example.com", i),
		}
		err = profileRepo.Create(ctx, profile)
		if err != nil {
			b.Fatalf("Failed to create benchmark profile: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := userRepo.FindById(ctx, userIDs[i%100], WithRelations("Profile"))
		if err != nil {
			b.Fatalf("FindById with relations failed: %v", err)
		}
	}
}

func BenchmarkEntityToMap_SmallFields(b *testing.B) {
	entity := tests.TestUser{
		ID:     uuid.New(),
		Name:   "Benchmark User",
		Email:  "benchmark@example.com",
		Age:    25,
		Active: true,
	}

	fields := map[string]interface{}{
		"Name":  nil,
		"Email": nil,
		"Age":   nil,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := utils.EntityToMap(fields, entity)
		if err != nil {
			b.Fatalf("EntityToMap failed: %v", err)
		}
	}
}

func BenchmarkEntityToMap_LargeFields(b *testing.B) {
	entity := tests.TestUser{
		ID:     uuid.New(),
		Name:   "Benchmark User",
		Email:  "benchmark@example.com",
		Age:    25,
		Active: true,
	}

	fields := map[string]interface{}{
		"ID":     nil,
		"Name":   nil,
		"Email":  nil,
		"Age":    nil,
		"Active": nil,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := utils.EntityToMap(fields, entity)
		if err != nil {
			b.Fatalf("EntityToMap failed: %v", err)
		}
	}
}
