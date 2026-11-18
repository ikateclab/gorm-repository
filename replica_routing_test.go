package gormrepository

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeStore struct {
	mu   sync.Mutex
	keys map[string]uint64
	err  error
}

func (s *fakeStore) Mark(_ context.Context, keys []string, lsn uint64, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		s.keys[k] = max(s.keys[k], lsn)
	}
	return nil
}

func (s *fakeStore) LastLsn(_ context.Context, keys []string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var lsn uint64
	for _, k := range keys {
		lsn = max(lsn, s.keys[k])
	}
	return lsn, s.err
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

type rrSetup struct {
	db       *gorm.DB
	store    *fakeStore
	sqls     *[]string
	lsnCalls *int
	routing  *ReplicaRouting
}

func setupRR(t *testing.T) rrSetup {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{keys: map[string]uint64{}}
	var lsnCalls int
	routing := &ReplicaRouting{Store: store, TTL: time.Second, CurrentLsn: func(context.Context) (uint64, error) {
		lsnCalls++
		return 0x1_0000_0000 + uint64(lsnCalls), nil
	}}
	if err := db.Use(routing); err != nil {
		t.Fatal(err)
	}
	var sqls []string
	db.Callback().Query().After("gorm:query").Register("test:capture", func(d *gorm.DB) { sqls = append(sqls, d.Statement.SQL.String()) })
	if err := db.AutoMigrate(&rrDepartment{}, &rrContact{}); err != nil {
		t.Fatal(err)
	}
	return rrSetup{db: db, store: store, sqls: &sqls, lsnCalls: &lsnCalls, routing: routing}
}

func (s rrSetup) last() string { return (*s.sqls)[len(*s.sqls)-1] }

// clear forgets the writes and queries of the setup.
func (s rrSetup) clear() {
	clear(s.store.keys)
	*s.sqls = nil
	*s.lsnCalls = 0
}

const firstLsnHint = "/* pgdog_role: replica pgdog_lsn: 1/1 */ "

func hintOf(sql string) string {
	if !strings.HasPrefix(sql, "/*") {
		return ""
	}
	return sql[:strings.Index(sql, "*/")+3]
}

func TestReplicaRouting(t *testing.T) {
	s := setupRR(t)
	repo := NewGormRepository[rrContact](s.db)
	acc, other := uuid.New(), uuid.New()
	ctx := context.Background()
	ofAccount := func(id uuid.UUID) Option {
		return WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, id) })
	}

	_, _ = repo.FindMany(ctx, ofAccount(acc))
	if hint := hintOf(s.last()); hint != replicaHint {
		t.Fatalf("read without recent write should go to any replica: %s", s.last())
	}

	tx := repo.BeginTransaction()
	if err := repo.Create(ctx, &rrContact{ID: uuid.New(), AccountId: acc}, WithTx(tx)); err != nil {
		t.Fatal(err)
	}
	if len(s.store.keys) != 0 || *s.lsnCalls != 0 {
		t.Fatalf("marked before commit: %v", s.store.keys)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if s.store.keys["last_write:"+acc.String()+":rr_contacts"] != 0x1_0000_0001 {
		t.Fatalf("not marked with the commit LSN: %v", s.store.keys)
	}

	_, _ = repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(map[string]interface{}{"accountId": acc}) }))
	if hint := hintOf(s.last()); hint != firstLsnHint {
		t.Fatalf("account with recent write should wait for its LSN: %s", s.last())
	}

	_, _ = repo.FindMany(ctx, ofAccount(other))
	if hint := hintOf(s.last()); hint != replicaHint {
		t.Fatalf("another account should not wait for the write: %s", s.last())
	}

	deps := NewGormRepository[rrDepartment](s.db)
	_, _ = deps.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, acc) }))
	if hint := hintOf(s.last()); hint != replicaHint {
		t.Fatalf("another table of the same account should not wait for the write: %s", s.last())
	}

	_, _ = repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Joins("Department").Where(`"rr_contacts"."accountId" = ?`, other) }))
	if hint := hintOf(s.last()); hint != replicaHint {
		t.Fatalf("known join without recent write should go to any replica: %s", s.last())
	}

	_, _ = repo.FindMany(ctx)
	if hint := hintOf(s.last()); hint != "" {
		t.Fatalf("read without account should go to the primary: %s", s.last())
	}

	s.db.Exec(`UPDATE rr_departments SET "accountId" = ?`, other)
	if s.store.keys["last_write:_global:rr_departments"] == 0 {
		t.Fatalf("raw write should mark the global scope: %v", s.store.keys)
	}
}

func TestReplicaRoutingTransactionTakesOneLsn(t *testing.T) {
	s := setupRR(t)
	contacts := NewGormRepository[rrContact](s.db)
	deps := NewGormRepository[rrDepartment](s.db)
	acc := uuid.New()
	ctx := context.Background()

	tx := contacts.BeginTransaction()
	if err := deps.Create(ctx, &rrDepartment{ID: uuid.New(), AccountId: acc}, WithTx(tx)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := contacts.Create(ctx, &rrContact{ID: uuid.New(), AccountId: acc}, WithTx(tx)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if *s.lsnCalls != 1 {
		t.Fatalf("expected one LSN for the whole transaction, got %d", *s.lsnCalls)
	}
	for _, table := range []string{"rr_contacts", "rr_departments"} {
		if s.store.keys["last_write:"+acc.String()+":"+table] != 0x1_0000_0001 {
			t.Fatalf("%s not marked with the commit LSN: %v", table, s.store.keys)
		}
	}
}

func TestReplicaRoutingUnknownLsnReadsFromThePrimary(t *testing.T) {
	s := setupRR(t)
	repo := NewGormRepository[rrContact](s.db)
	ctx := context.Background()
	key := func(acc uuid.UUID) string { return "last_write:" + acc.String() + ":rr_contacts" }

	inGormTx := uuid.New()
	if err := s.db.Transaction(func(gtx *gorm.DB) error {
		return gtx.Create(&rrContact{ID: uuid.New(), AccountId: inGormTx}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if s.store.keys[key(inGormTx)] != unknownLsn || *s.lsnCalls != 0 {
		t.Fatalf("a plain GORM transaction should mark an unknown LSN: %v", s.store.keys)
	}

	s.routing.CurrentLsn = func(context.Context) (uint64, error) { return 0, errors.New("primary down") }
	withoutLsn := uuid.New()
	if err := repo.Create(ctx, &rrContact{ID: uuid.New(), AccountId: withoutLsn}); err != nil {
		t.Fatal(err)
	}
	if s.store.keys[key(withoutLsn)] != unknownLsn {
		t.Fatalf("a failed LSN query should mark an unknown LSN: %v", s.store.keys)
	}

	_, _ = repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, withoutLsn) }))
	if hint := hintOf(s.last()); hint != "/* pgdog_role: replica pgdog_lsn: FFFFFFFF/FFFFFFFF */ " {
		t.Fatalf("an unknown LSN should exclude every replica: %s", s.last())
	}
}

func TestReplicaRoutingStoreErrorReadsFromThePrimary(t *testing.T) {
	s := setupRR(t)
	s.store.err = errors.New("redis down")

	_, _ = NewGormRepository[rrContact](s.db).FindMany(context.Background(),
		WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, uuid.New()) }))
	if hint := hintOf(s.last()); hint != "" {
		t.Fatalf("a store error should send the read to the primary: %s", s.last())
	}
}

type lsnArg struct{}

// argsPool records the args of each read and drops lsnArg, which SQLite does not take.
type argsPool struct {
	gorm.ConnPool
	args *[][]any
}

func (p argsPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	*p.args = append(*p.args, args)
	return p.ConnPool.QueryContext(ctx, query, slices.DeleteFunc(slices.Clone(args), func(a any) bool { return a == lsnArg{} })...)
}

func TestReplicaRoutingLsnReadArgs(t *testing.T) {
	s := setupRR(t)
	s.routing.LsnReadArgs = []any{lsnArg{}}
	var args [][]any
	pool := argsPool{ConnPool: s.db.ConnPool, args: &args}
	s.db.ConnPool, s.db.Statement.ConnPool = pool, pool
	var logged []any
	s.db.Callback().Query().After("replica:drop_query_args").Register("test:vars", func(d *gorm.DB) { logged = d.Statement.Vars })
	repo := NewGormRepository[rrContact](s.db)
	acc := uuid.New()
	ctx := context.Background()
	read := func() []any {
		if _, err := repo.FindMany(ctx, WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, acc) })); err != nil {
			t.Fatal(err)
		}
		return args[len(args)-1]
	}

	if got := read(); len(got) != 1 {
		t.Fatalf("a read without LSN should keep its args: %v", got)
	}
	if err := repo.Create(ctx, &rrContact{ID: uuid.New(), AccountId: acc}); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 2 || got[0] != (lsnArg{}) {
		t.Fatalf("a read with LSN should lead with LsnReadArgs: %v", got)
	}
	if len(logged) != 1 || logged[0] != acc {
		t.Fatalf("the statement should keep only its own vars after the read: %v", logged)
	}
}

func TestReplicaRoutingWithoutStore(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Use(&ReplicaRouting{TTL: time.Second}); err == nil {
		t.Fatal("registering without a store should fail")
	}
	if db.Callback().Query().Get("replica:route_query") != nil {
		t.Fatal("a failed registration should leave no callback")
	}
}

func TestReplicaRoutingPreloadTakesTheAccount(t *testing.T) {
	s := setupRR(t)
	repo := NewGormRepository[rrContact](s.db)
	acc := uuid.New()
	dep := rrDepartment{ID: uuid.New(), AccountId: acc}
	contact := rrContact{ID: uuid.New(), AccountId: acc, DepartmentId: dep.ID}
	s.db.Create(&dep)
	s.db.Create(&contact)
	ctx := context.Background()
	ofAccount := WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, acc) })

	s.clear()
	if _, err := repo.FindById(ctx, contact.ID, ofAccount, WithRelations("Department")); err != nil {
		t.Fatal(err)
	}
	if len(*s.sqls) != 2 {
		t.Fatalf("expected the contact and the preloaded department: %v", *s.sqls)
	}
	for _, sql := range *s.sqls {
		if hint := hintOf(sql); hint != replicaHint {
			t.Fatalf("read of an account without recent write, and its preload, should go to any replica: %s", sql)
		}
	}

	s.store.keys["last_write:"+acc.String()+":rr_departments"] = 0x1_0000_0001
	*s.sqls = nil
	if _, err := repo.FindById(ctx, contact.ID, ofAccount, WithRelations("Department")); err != nil {
		t.Fatal(err)
	}
	for _, sql := range *s.sqls {
		if strings.Contains(sql, "rr_departments") != (hintOf(sql) == firstLsnHint) {
			t.Fatalf("only the preload of the recently written table should wait for its LSN: %v", *s.sqls)
		}
	}

	s.clear()
	if _, err := repo.FindById(ctx, contact.ID, WithRelations("Department")); err != nil {
		t.Fatal(err)
	}
	for _, sql := range *s.sqls {
		if hint := hintOf(sql); hint != "" {
			t.Fatalf("read by id without the account, and its preload, should go to the primary: %s", sql)
		}
	}
}

type rrNote struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	ContactId uuid.UUID `gorm:"column:contactId"`
}

type rrContactWithNotes struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	AccountId uuid.UUID `gorm:"column:accountId"`
	Notes     []rrNote  `gorm:"foreignKey:ContactId"`
}

func (rrContactWithNotes) TableName() string { return "rr_contacts" }

func TestReplicaRoutingPreloadOfTableWithoutAccount(t *testing.T) {
	s := setupRR(t)
	if err := s.db.AutoMigrate(&rrNote{}); err != nil {
		t.Fatal(err)
	}
	repo := NewGormRepository[rrContactWithNotes](s.db)
	notes := NewGormRepository[rrNote](s.db)
	ctx := context.Background()
	contact := rrContactWithNotes{ID: uuid.New(), AccountId: uuid.New()}
	s.db.Create(&contact)
	ofAccount := WithQuery(func(d *gorm.DB) *gorm.DB { return d.Where(`"accountId" = ?`, contact.AccountId) })

	s.clear()
	if err := notes.Create(ctx, &rrNote{ID: uuid.New(), ContactId: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	if s.store.keys["last_write:_global:rr_notes"] == 0 {
		t.Fatalf("write to a table without accountId should mark the global scope: %v", s.store.keys)
	}

	*s.sqls = nil
	if _, err := repo.FindById(ctx, contact.ID, ofAccount, WithRelations("Notes")); err != nil {
		t.Fatal(err)
	}
	for _, sql := range *s.sqls {
		if strings.Contains(sql, "rr_notes") != (hintOf(sql) == firstLsnHint) {
			t.Fatalf("preload of a table without accountId written by any account should wait for its LSN: %v", *s.sqls)
		}
	}
}

func TestReplicaRoutingRollbackDoesNotMark(t *testing.T) {
	s := setupRR(t)
	repo := NewGormRepository[rrContact](s.db)

	tx := repo.BeginTransaction()
	if err := repo.Create(context.Background(), &rrContact{ID: uuid.New(), AccountId: uuid.New()}, WithTx(tx)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if len(s.store.keys) != 0 || *s.lsnCalls != 0 {
		t.Fatalf("rollback should not mark: %v", s.store.keys)
	}
}

func TestReplicaRoutingWritesDuringAnLsnQueryShareTheNext(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Uint64
	r := &ReplicaRouting{CurrentLsn: func(context.Context) (uint64, error) {
		started <- struct{}{}
		<-release
		return calls.Add(1), nil
	}}
	ctx := context.Background()

	running := r.joinLsnBatch(ctx)
	<-started
	during := []*lsnBatch{r.joinLsnBatch(ctx), r.joinLsnBatch(ctx)}
	close(release)
	<-running.done
	<-during[0].done

	if during[0] != during[1] || during[0] == running {
		t.Fatal("writes committed during a query should share the next one")
	}
	if running.lsn != 1 || during[0].lsn != 2 || calls.Load() != 2 {
		t.Fatalf("lsn running=%d during=%d calls=%d", running.lsn, during[0].lsn, calls.Load())
	}
}

func TestParseLsn(t *testing.T) {
	lsn, err := parseLsn("1A/2B3C4D5E")
	if err != nil || lsn != 0x1A_2B3C4D5E {
		t.Fatalf("got %X, %v", lsn, err)
	}
	for _, invalid := range []string{"", "1A", "1A/", "G/1", "100000000/0"} {
		if _, err := parseLsn(invalid); err == nil {
			t.Fatalf("%q should be invalid", invalid)
		}
	}
}
