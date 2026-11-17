package gormrepository

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeStore struct {
	mu   sync.Mutex
	keys map[string]bool
}

func (s *fakeStore) Mark(_ context.Context, key string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[key] = true
	return nil
}

func (s *fakeStore) AnyRecent(_ context.Context, keys []string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if s.keys[k] {
			return true, nil
		}
	}
	return false, nil
}

type rrDepartment struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	AccountId uuid.UUID `gorm:"column:accountId"`
}

type rrContact struct {
	ID           uuid.UUID     `gorm:"type:uuid;primaryKey"`
	AccountId    uuid.UUID     `gorm:"column:accountId"`
	DepartmentId uuid.UUID     `gorm:"column:departmentId"`
	Department   *rrDepartment `gorm:"foreignKey:DepartmentId"`
}

func setupRR(t *testing.T) (*gorm.DB, *fakeStore, *[]string) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{keys: map[string]bool{}}
	if err := db.Use(&ReplicaRouting{Store: store, TTL: 300 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	var sqls []string
	db.Callback().Query().After("gorm:query").Register("test:capture", func(d *gorm.DB) { sqls = append(sqls, d.Statement.SQL.String()) })
	if err := db.AutoMigrate(&rrDepartment{}, &rrContact{}); err != nil {
		t.Fatal(err)
	}
	return db, store, &sqls
}

func last(s *[]string) string { return (*s)[len(*s)-1] }

func TestReplicaRouting(t *testing.T) {
	db, store, sqls := setupRR(t)
	repo := NewGormRepository[rrContact](db)
	acc, other := uuid.New(), uuid.New()
	ctx := context.Background()

	_, _ = repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, acc) }))
	if strings.HasPrefix(last(sqls), primaryHint) {
		t.Fatalf("read without recent write should go to the replica: %s", last(sqls))
	}

	tx := repo.BeginTransaction()
	if err := repo.Create(ctx, &rrContact{ID: uuid.New(), AccountId: acc}, WithTx(tx)); err != nil {
		t.Fatal(err)
	}
	if len(store.keys) != 0 {
		t.Fatalf("marked before commit: %v", store.keys)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !store.keys["last_write:"+acc.String()+":rr_contacts"] {
		t.Fatalf("not marked on commit: %v", store.keys)
	}

	_, _ = repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(map[string]interface{}{"accountId": acc}) }))
	if !strings.HasPrefix(last(sqls), primaryHint) {
		t.Fatalf("account with recent write should go to the primary: %s", last(sqls))
	}

	_, _ = repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, other) }))
	if strings.HasPrefix(last(sqls), primaryHint) {
		t.Fatalf("another account should not go to the primary: %s", last(sqls))
	}

	deps := NewGormRepository[rrDepartment](db)
	_, _ = deps.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, acc) }))
	if strings.HasPrefix(last(sqls), primaryHint) {
		t.Fatalf("another table of the same account should not go to the primary: %s", last(sqls))
	}

	_, _ = repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Joins("Department").Where(`"rr_contacts"."accountId" = ?`, other) }))
	if strings.HasPrefix(last(sqls), primaryHint) {
		t.Fatalf("known join without recent write should go to the replica: %s", last(sqls))
	}

	_, _ = repo.FindMany(ctx)
	if !strings.HasPrefix(last(sqls), primaryHint) {
		t.Fatalf("read without account should go to the primary: %s", last(sqls))
	}

	db.Exec(`UPDATE rr_departments SET "accountId" = ?`, other)
	if !store.keys["last_write:_global:rr_departments"] {
		t.Fatalf("raw write should mark the global scope: %v", store.keys)
	}
}

func TestReplicaRoutingRollbackDoesNotMark(t *testing.T) {
	db, store, _ := setupRR(t)
	repo := NewGormRepository[rrContact](db)

	tx := repo.BeginTransaction()
	if err := repo.Create(context.Background(), &rrContact{ID: uuid.New(), AccountId: uuid.New()}, WithTx(tx)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(store.keys) != 0 {
		t.Fatalf("rollback should not mark: %v", store.keys)
	}
}
