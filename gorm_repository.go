package gormrepository

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/ikateclab/gorm-repository/utils"
)

const (
	txContextKey = "__tx"
)

type GormRepository[T any] struct {
	Repository[T]
	DB *gorm.DB
}

// NewGormRepository creates a new instance of GormRepository with the provided GORM database connection.
// T is the entity type that this repository will manage.
func NewGormRepository[T any](db *gorm.DB) *GormRepository[T] {
	return &GormRepository[T]{
		DB: db,
	}
}

func WithRelations(relations ...string) Option {
	return func(db *gorm.DB) *gorm.DB {
		for _, relation := range relations {
			db = db.Preload(relation)
		}
		return db
	}
}

func applyOptions(db *gorm.DB, options []Option) *gorm.DB {
	for _, option := range options {
		if option != nil {
			db = option(db)
		}
	}
	return db
}

func newEntity[T any]() T {
	var entity T
	entityType := reflect.TypeOf(entity)
	if entityType.Kind() == reflect.Ptr {
		return reflect.New(entityType.Elem()).Interface().(T)
	}
	return entity
}

func (r *GormRepository[T]) FindMany(ctx context.Context, options ...Option) ([]*T, error) {
	var entities []*T
	db := applyOptions(r.DB, options).WithContext(ctx)
	if err := db.Find(&entities).Error; err != nil {
		return nil, err
	}

	return entities, nil
}

// FindPaginated retrieves records with pagination.
func (r *GormRepository[T]) FindPaginated(ctx context.Context, page int, pageSize int, options ...Option) (*PaginationResult[*T], error) {
	var entities []*T
	var totalRows int64

	db := applyOptions(r.DB, options).WithContext(ctx)
	db.Model(&entities).Count(&totalRows)

	offset := (page - 1) * pageSize
	if err := db.Offset(offset).Limit(pageSize).Find(&entities).Error; err != nil {
		return nil, err
	}

	result := &PaginationResult[*T]{
		Data:        entities,
		Total:       totalRows,
		Limit:       pageSize,
		Offset:      offset,
		CurrentPage: page,
		LastPage:    int((totalRows + int64(pageSize) - 1) / int64(pageSize)),
		From:        offset + 1,
		To:          offset + len(entities),
	}

	return result, nil
}

func (r *GormRepository[T]) FindOne(ctx context.Context, options ...Option) (*T, error) {
	entity := newEntity[T]()
	db := applyOptions(r.DB, options).WithContext(ctx)

	if err := db.First(&entity).Error; err != nil {
		return nil, err
	}

	// Store clone if in transaction and supports cloning
	storeCloneIfInTransaction(db, entity)

	return &entity, nil
}

func (r *GormRepository[T]) FindById(ctx context.Context, id uuid.UUID, options ...Option) (*T, error) {
	entity := newEntity[T]()
	db := applyOptions(r.DB, options).WithContext(ctx)
	if err := db.First(&entity, "id = ?", id).Error; err != nil {
		return nil, err
	}

	// Store clone if in transaction and supports cloning
	storeCloneIfInTransaction(db, entity)

	return &entity, nil
}

func (r *GormRepository[T]) Create(ctx context.Context, entity *T, options ...Option) error {
	db := applyOptions(r.DB, options).WithContext(ctx)
	if err := db.Create(entity).Error; err != nil {
		return err
	}

	storeCloneIfInTransaction(db, entity)

	return nil
}

func (r *GormRepository[T]) Save(ctx context.Context, entity *T, options ...Option) error {
	db := applyOptions(r.DB, options).WithContext(ctx)
	return db.Save(entity).Error
}

func (r *GormRepository[T]) UpdateByIdWithMap(ctx context.Context, id uuid.UUID, values map[string]interface{}, options ...Option) (*T, error) {
	db := applyOptions(r.DB, options).WithContext(ctx)
	entity := newEntity[T]()

	if err := db.Model(&entity).Where("id = ?", id).Updates(values).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

func (r *GormRepository[T]) UpdateByIdWithMask(ctx context.Context, id uuid.UUID, mask map[string]interface{}, entity *T, options ...Option) error {
	db := applyOptions(r.DB, options).WithContext(ctx)

	updateMap, err := utils.EntityToMap(mask, entity)
	if err != nil {
		return err
	}

	return db.Model(entity).Clauses(clause.Returning{}).Where("id = ?", id).Updates(updateMap).Error
}

// getCloneForDiff attempts to get an existing clone from transaction context,
// falling back to a blank entity if no clone is available
func getCloneForDiff[T any](db *gorm.DB, entity T) T {
	// Try to get transaction context
	txInterface, exists := db.Get(txContextKey)
	if !exists {
		return newEntity[T]()
	}

	tx, ok := txInterface.(*Tx)
	if !ok {
		return newEntity[T]()
	}

	// Try to get cloned entity from transaction
	cloneInterface, found := tx.getClonedEntity(generateEntityKey(entity))
	if !found {
		return newEntity[T]()
	}

	clone, ok := cloneInterface.(T)
	if !ok {
		return newEntity[T]()
	}

	return clone
}

func (r *GormRepository[T]) UpdateById(ctx context.Context, id uuid.UUID, entity *T, options ...Option) error {
	db := applyOptions(r.DB, options).WithContext(ctx)

	// Generate diff
	diffable, ok := any(entity).(Diffable[T])
	if !ok {
		return fmt.Errorf("entity must implement Diffable[T] interface")
	}

	clone := getCloneForDiff(db, entity)

	diff := diffable.Diff(clone)
	if len(diff) == 0 {
		return nil // No changes
	}

	return db.Model(entity).Clauses(clause.Returning{}).Where("id = ?", id).Updates(diff).Error
}

func (r *GormRepository[T]) UpdateByIdInPlace(ctx context.Context, id uuid.UUID, entity *T, updateFunc func(), options ...Option) error {
	db := applyOptions(r.DB, options).WithContext(ctx)

	diffable, isDiffable := any(entity).(Diffable[T])
	if !isDiffable {
		return fmt.Errorf("entity does not support diffing - entity must implement Diffable[T] interface")
	}

	// Clone the original entity to use for diff generation
	originalClone := diffable.Clone()

	// Apply the update function to modify the entity in place
	updateFunc()

	diff := diffable.Diff(originalClone)

	if len(diff) == 0 {
		// No changes, nothing to update
		return nil
	}

	// Perform the update using the diff and return the updated entity
	return db.Model(entity).Clauses(clause.Returning{}).Where("id = ?", id).Updates(diff).Error
}

func (r *GormRepository[T]) UpdateInPlace(ctx context.Context, entity *T, updateFunc func(), options ...Option) error {
	db := applyOptions(r.DB, options).WithContext(ctx)

	diffable, isDiffable := any(entity).(Diffable[T])
	if !isDiffable {
		return fmt.Errorf("entity does not support diffing - entity must implement Diffable[T] interface")
	}

	// Clone the original entity to use for diff generation
	originalClone := diffable.Clone()

	// Apply the update function to modify the entity in place
	updateFunc()

	diff := diffable.Diff(originalClone)

	if len(diff) == 0 {
		// No changes, nothing to update
		return nil
	}

	// Perform the update using the diff - GORM will extract the primary key from the entity
	return db.Model(entity).Clauses(clause.Returning{}).Updates(diff).Error
}

func (r *GormRepository[T]) DeleteById(ctx context.Context, id uuid.UUID, options ...Option) error {
	db := applyOptions(r.DB, options).WithContext(ctx)
	return db.Delete(new(T), "id = ?", id).Error
}

func (r *GormRepository[T]) AppendAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...Option) error {
	return applyOptions(r.DB, options).
		WithContext(ctx).
		Model(entity).
		Omit(association + ".*"). // https://gorm.io/docs/associations.html#Using-Omit-to-Exclude-Fields-or-Associations
		Association(association).
		Append(values)
}

func (r *GormRepository[T]) RemoveAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...Option) error {
	return applyOptions(r.DB, options).
		WithContext(ctx).
		Model(entity).
		Association(association).
		Delete(values)
}

func (r *GormRepository[T]) ReplaceAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...Option) error {
	return applyOptions(r.DB, options).
		WithContext(ctx).
		Model(entity).
		Omit(association + ".*").
		Association(association).
		Replace(values)
}

func (r *GormRepository[T]) GetDB() *gorm.DB {
	return r.DB
}

// BeginTransaction starts a new transaction that should be used with defer for automatic cleanup
func (r *GormRepository[T]) BeginTransaction() *Tx {
	gtx := r.DB.Begin()
	return &Tx{
		gtx:            gtx,
		committed:      false,
		rolledBack:     false,
		clonedEntities: make(map[string]interface{}),
	}
}

// WithTx returns an option to run the query within a transaction.
// When used with Find operations, it automatically clones entities that support cloning.
func WithTx(tx *Tx) Option {
	return func(db *gorm.DB) *gorm.DB {
		// Store the transaction reference in the context for later use
		return tx.gtx.Set(txContextKey, tx)
	}
}

// WithQuery returns an option to customize the query.
func WithQuery(fn func(*gorm.DB) *gorm.DB) Option {
	return func(db *gorm.DB) *gorm.DB {
		return fn(db)
	}
}

func WithQueryStruct(query map[string]interface{}) Option {
	return func(db *gorm.DB) *gorm.DB {
		return db.Where(query)
	}
}

type Tx struct {
	gtx        *gorm.DB
	committed  bool
	rolledBack bool
	// clonedEntities stores cloned entities as snapshots during transaction
	// key is a unique identifier for the entity, value is the cloned entity snapshot
	clonedEntities map[string]interface{}
	mutex          sync.RWMutex
}

// BeginTransaction starts a nested transaction
func (tx *Tx) BeginTransaction() *Tx {
	gtx := tx.gtx.Begin()
	return &Tx{
		gtx:            gtx,
		committed:      false,
		rolledBack:     false,
		clonedEntities: make(map[string]interface{}),
	}
}

// Commit commits the transaction
func (tx *Tx) Commit() error {
	if tx.committed || tx.rolledBack {
		return nil
	}

	err := tx.gtx.Commit().Error
	if err == nil {
		tx.committed = true
	}
	return err
}

// Rollback rolls back the transaction
func (tx *Tx) Rollback() error {
	if tx.committed || tx.rolledBack {
		return nil
	}

	err := tx.gtx.Rollback().Error
	if err == nil {
		tx.rolledBack = true
	}
	return err
}

// Finish should be called with defer to automatically handle commit/rollback
// Usage: defer tx.Finish(&err)
// Use this for simple cases where you don't need complex error handling
// Will commit if err is nil, rollback if err is set
func (tx *Tx) Finish(err *error) {
	if tx.committed || tx.rolledBack {
		return
	}

	if *err != nil {
		// If there was an error, rollback
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			// Log rollback error but don't override the original error
			// You might want to use your logging framework here
		}
	} else {
		// If no error, commit
		if commitErr := tx.Commit(); commitErr != nil {
			*err = commitErr
		}
	}
}

// Error returns any error from the underlying GORM transaction
func (tx *Tx) Error() error {
	return tx.gtx.Error
}

// storeClonedEntity stores the original entity before cloning
func (tx *Tx) storeClonedEntity(entityKey string, original interface{}) {
	tx.mutex.Lock()
	defer tx.mutex.Unlock()
	tx.clonedEntities[entityKey] = original
}

// getClonedEntity retrieves the original entity if it was cloned
func (tx *Tx) getClonedEntity(entityKey string) (interface{}, bool) {
	tx.mutex.RLock()
	defer tx.mutex.RUnlock()
	original, exists := tx.clonedEntities[entityKey]
	return original, exists
}

// generateEntityKey creates a unique key for an entity based on its type and Id
func generateEntityKey(entity interface{}) string {
	entityType := reflect.TypeOf(entity)
	if entityType.Kind() == reflect.Ptr {
		entityType = entityType.Elem()
	}

	// Try to get Id field using reflection
	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	idField := entityValue.FieldByName("Id")
	if !idField.IsValid() {
		// Fallback to memory address if no Id field
		return fmt.Sprintf("%s_%p", entityType.Name(), entity)
	}

	return fmt.Sprintf("%s_%v", entityType.Name(), idField.Interface())
}

// storeCloneIfInTransaction stores a clone of the entity if we're in a transaction and the entity supports cloning
func storeCloneIfInTransaction[T any](db *gorm.DB, entity T) {
	// Check if we're in a transaction context
	txInterface, exists := db.Get(txContextKey)
	if !exists {
		return
	}

	tx, ok := txInterface.(*Tx)
	if !ok {
		return
	}

	// Check if entity supports cloning
	cloneable, ok := any(entity).(Diffable[T])
	if !ok {
		return
	}

	// Store the cloned entity as a snapshot
	entityKey := generateEntityKey(entity)
	clone := cloneable.Clone()
	tx.storeClonedEntity(entityKey, clone)
}
