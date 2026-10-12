package rediscache

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ikateclab/gorm-repository/plugin"
	"github.com/ikateclab/gorm-repository/plugin/memory"
)

// benchRow is a typical row: a uuid key, a few strings and ints, timestamps.
type benchRow struct {
	Id        uuid.UUID `gorm:"type:uuid;primaryKey"`
	AccountId uuid.UUID `gorm:"type:uuid;index"`
	Name      string
	Email     string
	Status    string
	Score     int
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (benchRow) TableName() string { return "bench_rows" }

// setupPostgres starts the same Postgres image the root package's tests use
// and returns its DSN.
func setupPostgres(tb testing.TB) string {
	tb.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:18beta1-alpine3.21",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "postgres",
				"POSTGRES_PASSWORD": "secret",
				"POSTGRES_DB":       "bench",
			},
			// Postgres logs this once for its init run and once for real.
			WaitingFor: wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	require.NoError(tb, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(tb, err)
	return fmt.Sprintf("host=%s port=%s user=postgres password=secret dbname=bench sslmode=disable", host, port.Port())
}

func openBenchDB(tb testing.TB, dsn string, cache plugin.Cache) *gorm.DB {
	tb.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(tb, err)
	if cache != nil {
		require.NoError(tb, db.Use(plugin.New(cache, plugin.WithDefaultTTL(10*time.Minute))))
	}
	return db
}

// BenchmarkCache measures what the cache costs and saves against a real
// Postgres, with Redis in a container next to it: both are local, so the
// numbers compare round trips of similar latency, not production ones.
//
//	go test -run '^$' -bench BenchmarkCache -benchmem ./plugin/rediscache/
func BenchmarkCache(b *testing.B) {
	ctx := context.Background()
	dsn := setupPostgres(b)
	client := setupRedis(b)

	plain := openBenchDB(b, dsn, nil)
	withMemory := openBenchDB(b, dsn, memory.New())
	withRedis := openBenchDB(b, dsn, New(client))
	require.NoError(b, plain.AutoMigrate(&benchRow{}))

	account := uuid.New()
	rows := make([]benchRow, 100)
	for i := range rows {
		rows[i] = benchRow{
			Id: uuid.New(), AccountId: account, Name: fmt.Sprintf("user %d", i),
			Email: fmt.Sprintf("user%d@example.com", i), Status: "active", Score: i,
		}
	}
	require.NoError(b, plain.Create(&rows).Error)
	id := rows[0].Id

	findByID := func(db *gorm.DB) func(*testing.B) {
		return func(b *testing.B) {
			var warm benchRow
			require.NoError(b, db.First(&warm, "id = ?", id).Error)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var r benchRow
				if err := db.First(&r, "id = ?", id).Error; err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	find100 := func(db *gorm.DB) func(*testing.B) {
		return func(b *testing.B) {
			var warm []benchRow
			require.NoError(b, db.Where("account_id = ?", account).Find(&warm).Error)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var rs []benchRow
				if err := db.Where("account_id = ?", account).Find(&rs).Error; err != nil {
					b.Fatal(err)
				}
			}
		}
	}
	update := func(db *gorm.DB) func(*testing.B) {
		return func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := db.Model(&benchRow{Id: id}).Update("score", i).Error; err != nil {
					b.Fatal(err)
				}
			}
		}
	}

	b.Run("FindById/postgres-no-cache", findByID(plain))
	b.Run("FindById/memory-hit", findByID(withMemory))
	b.Run("FindById/redis-hit", findByID(withRedis))
	b.Run("FindById/redis-miss", func(b *testing.B) {
		// Lookup, query and store: what the first read of a key costs.
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			require.NoError(b, client.FlushDB(ctx).Err())
			b.StartTimer()
			var r benchRow
			if err := withRedis.First(&r, "id = ?", id).Error; err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Find100/postgres-no-cache", find100(plain))
	b.Run("Find100/redis-hit", find100(withRedis))
	b.Run("Update/no-cache", update(plain))
	b.Run("Update/redis-invalidate", update(withRedis))
}
