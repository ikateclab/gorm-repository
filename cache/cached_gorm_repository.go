package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"gorm.io/gorm"

	gormrepository "github.com/ikateclab/gorm-repository"
)

// CachedGormRepository extends GormRepository with caching capabilities
type CachedGormRepository[T any] struct {
	*gormrepository.GormRepository[T]
	cache           ResourceCacheInterface
	dbSchemaVersion string
	debugEnabled    bool
}

// NewCachedGormRepository creates a new cached repository
func NewCachedGormRepository[T any](db *gorm.DB, ResourceCache *ResourceCache, dbSchemaVersion string, debugEnabled bool) *CachedGormRepository[T] {
	return &CachedGormRepository[T]{
		GormRepository:  gormrepository.NewGormRepository[T](db),
		cache:           ResourceCache,
		dbSchemaVersion: dbSchemaVersion,
		debugEnabled:    debugEnabled,
	}
}

// NewCachedGormRepositoryWithCache creates a new cached repository with a custom cache interface
func NewCachedGormRepositoryWithCache[T any](db *gorm.DB, cache ResourceCacheInterface, dbSchemaVersion string, debugEnabled bool) *CachedGormRepository[T] {
	return &CachedGormRepository[T]{
		GormRepository:  gormrepository.NewGormRepository[T](db),
		cache:           cache,
		dbSchemaVersion: dbSchemaVersion,
		debugEnabled:    debugEnabled,
	}
}

// Helper functions equivalent to the Node.js implementation

func getAccountIdsFromData[T any](data interface{}) []string {
	var accountIds []string

	switch v := data.(type) {
	case []T:
		for _, item := range v {
			ids := getAccountIdsFromSingleData(item)
			accountIds = append(accountIds, ids...)
		}
	default:
		accountIds = getAccountIdsFromSingleData(v)
	}

	return accountIds
}

func getAccountIdsFromSingleData(data interface{}) []string {
	val := reflect.ValueOf(data)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if !val.IsValid() || val.Kind() != reflect.Struct {
		return []string{}
	}

	accountIdField := val.FieldByName("AccountId")
	if !accountIdField.IsValid() {
		return []string{}
	}

	if accountIdField.Kind() == reflect.String {
		accountId := accountIdField.String()
		if accountId != "" {
			return []string{accountId}
		}
	}

	return []string{}
}

func getAccountIdsFromQuery(query map[string]interface{}) string {
	if where, ok := query["where"]; ok {
		switch w := where.(type) {
		case map[string]interface{}:
			if accountId, exists := w["accountId"]; exists {
				if id, ok := accountId.(string); ok {
					return id
				}
			}
		case []map[string]interface{}:
			for _, condition := range w {
				if accountId, exists := condition["accountId"]; exists {
					if id, ok := accountId.(string); ok {
						return id
					}
				}
			}
		}
	}
	return ""
}

func makeListKeyWithAccountId(accountId string) string {
	if accountId != "" {
		return fmt.Sprintf("%s:list", accountId)
	}
	return "no-account:list"
}

func (r *CachedGormRepository[T]) getResourceName() string {
	var entity T
	entityType := reflect.TypeOf(entity)
	if entityType.Kind() == reflect.Ptr {
		entityType = entityType.Elem()
	}
	return entityType.Name()
}

func (r *CachedGormRepository[T]) makeKey(key string) string {
	return fmt.Sprintf("%s:%s", r.getResourceName(), key)
}

func (r *CachedGormRepository[T]) makeListKeyFromQuery(query map[string]interface{}) string {
	accountId := getAccountIdsFromQuery(query)
	return r.makeKey(makeListKeyWithAccountId(accountId))
}

func (r *CachedGormRepository[T]) makeListKeyFromData(data interface{}) []string {
	accountIds := getAccountIdsFromData[T](data)

	var ids []string
	switch v := data.(type) {
	case []T:
		for _, item := range v {
			if id := r.getEntityId(item); id != "" {
				ids = append(ids, id)
			}
		}
	default:
		if id := r.getEntityId(v); id != "" {
			ids = append(ids, id)
		}
	}

	var keys []string
	for _, accountId := range accountIds {
		keys = append(keys, r.makeKey(makeListKeyWithAccountId(accountId)))
	}
	for _, id := range ids {
		keys = append(keys, r.makeKey(id))
	}

	return keys
}

func (r *CachedGormRepository[T]) getEntityId(entity interface{}) string {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if !val.IsValid() || val.Kind() != reflect.Struct {
		return ""
	}

	// Try Id first, then ID
	for _, fieldName := range []string{"Id", "ID"} {
		idField := val.FieldByName(fieldName)
		if idField.IsValid() {
			switch idField.Kind() {
			case reflect.String:
				return idField.String()
			default:
				return fmt.Sprintf("%v", idField.Interface())
			}
		}
	}

	return ""
}

func (r *CachedGormRepository[T]) parseQueryToKey(query map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	allowedKeys := []string{
		"attributes", "where", "include", "fields", "order",
		"subQuery", "through", "offset", "limit", "raw", "page", "perPage",
	}

	for _, key := range allowedKeys {
		if value, exists := query[key]; exists {
			result[key] = value
		}
	}

	return result
}

func (r *CachedGormRepository[T]) buildListTagsFromDataAndQuery(data []T, query map[string]interface{}) []RawTag {
	var tags []RawTag

	// Add entity IDs
	for _, item := range data {
		if id := r.getEntityId(item); id != "" {
			tags = append(tags, r.makeKey(id))
		}
	}

	// Add account-based list key
	tags = append(tags, r.makeListKeyFromQuery(query))

	return tags
}

func (r *CachedGormRepository[T]) buildSingleTagsFromDataAndQuery(id string, _ interface{}, _ map[string]interface{}) []RawTag {
	var tags []RawTag
	tags = append(tags, r.makeKey(id))
	return tags
}

// Cached repository methods

func (r *CachedGormRepository[T]) FindMany(ctx context.Context, options ...gormrepository.Option) ([]*T, error) {
	query := r.optionsToQuery(options)

	cacheKey := []interface{}{r.getResourceName(), r.parseQueryToKey(query)}

	result, err := r.cache.Remember(
		ctx,
		cacheKey,
		func() (interface{}, error) {
			return r.GormRepository.FindMany(ctx, options...)
		},
		func(value interface{}) ([]RawTag, error) {
			if data, ok := value.([]T); ok {
				return r.buildListTagsFromDataAndQuery(data, query), nil
			}
			return []RawTag{}, nil
		},
		nil,
	)

	if err != nil {
		return nil, err
	}

	// From database
	if data, ok := result.([]*T); ok {
		return data, nil
	}

	// From cache
	if data, ok := result.([]interface{}); ok {
		var entities []*T
		for _, item := range data {
			if mapItem, ok := item.(map[string]interface{}); ok {
				entity := newEntity[T]()
				jsonData, _ := json.Marshal(mapItem)
				json.Unmarshal(jsonData, &entity)
				entities = append(entities, &entity)
			}
		}
		return entities, nil
	}

	return []*T{}, nil
}
func (r *CachedGormRepository[T]) FindPaginated(ctx context.Context, page int, pageSize int, options ...gormrepository.Option) (*gormrepository.PaginationResult[*T], error) {
	query := r.optionsToQuery(options)
	query["page"] = page
	query["perPage"] = pageSize

	cacheKey := []interface{}{r.getResourceName(), r.parseQueryToKey(query)}

	result, err := r.cache.Remember(
		ctx,
		cacheKey,
		func() (interface{}, error) {
			return r.GormRepository.FindPaginated(ctx, page, pageSize, options...)
		},
		func(value interface{}) ([]RawTag, error) {
			if paginationResult, ok := value.(*gormrepository.PaginationResult[T]); ok {
				return r.buildListTagsFromDataAndQuery(paginationResult.Data, query), nil
			}
			return []RawTag{}, nil
		},
		nil,
	)

	if err != nil {
		return nil, err
	}

	// From database
	if paginationResult, ok := result.(gormrepository.PaginationResult[*T]); ok {
		return &paginationResult, nil
	}

	// From cache
	if resultMap, ok := result.(map[string]interface{}); ok {
		jsonData, _ := json.Marshal(resultMap)
		var paginationResult gormrepository.PaginationResult[*T]
		json.Unmarshal(jsonData, &paginationResult)
		return &paginationResult, nil
	}

	// Unexpected type
	return nil, fmt.Errorf("unexpected result type: %T", result)
}

func (r *CachedGormRepository[T]) FindById(ctx context.Context, id uuid.UUID, options ...gormrepository.Option) (*T, error) {
	optionsCopy := make([]gormrepository.Option, len(options))
	copy(optionsCopy, options)

	query := r.optionsToQuery(options)
	idStr := id.String()

	cacheKey := []interface{}{r.getResourceName(), idStr, r.parseQueryToKey(query)}

	rememberOptions := &RememberOptions{}

	tx := func() *gormrepository.Tx {
		db := r.applyOptionsToGetDB(optionsCopy)
		return gormrepository.GetTransactionFromDB(db)
	}()

	if tx != nil && tx.TransactionCacheInvalid {
		rememberOptions.SkipCache = true
	}

	result, err := r.cache.Remember(
		ctx,
		cacheKey,
		func() (interface{}, error) {
			return r.GormRepository.FindById(ctx, id, options...)
		},
		func(value interface{}) ([]RawTag, error) {
			return r.buildSingleTagsFromDataAndQuery(idStr, value, query), nil
		},
		rememberOptions,
	)

	if err != nil {
		return new(T), err
	}

	// From database
	if data, ok := result.(T); ok {
		return &data, nil
	}

	// From cache
	if data, ok := result.(map[string]interface{}); ok {
		entity := newEntity[T]()
		jsonData, _ := json.Marshal(data)
		json.Unmarshal(jsonData, &entity)
		return &entity, nil
	}

	return new(T), nil
}

func (r *CachedGormRepository[T]) FindOne(ctx context.Context, options ...gormrepository.Option) (*T, error) {
	query := r.optionsToQuery(options)

	cacheKey := []interface{}{r.getResourceName(), r.parseQueryToKey(query)}

	result, err := r.cache.Remember(
		ctx,
		cacheKey,
		func() (interface{}, error) {
			return r.GormRepository.FindOne(ctx, options...)
		},
		func(value interface{}) ([]RawTag, error) {
			if id := r.getEntityId(value); id != "" {
				return r.buildSingleTagsFromDataAndQuery(id, value, query), nil
			}
			return []RawTag{}, nil
		},
		nil,
	)

	if err != nil {
		return new(T), err
	}

	// From database
	if data, ok := result.(T); ok {
		return &data, nil
	}

	// From cache
	if data, ok := result.(map[string]interface{}); ok {
		entity := newEntity[T]()
		jsonData, _ := json.Marshal(data)
		json.Unmarshal(jsonData, &entity)
		return &entity, nil
	}

	return new(T), nil
}

// Write operations that invalidate cache

func (r *CachedGormRepository[T]) Create(ctx context.Context, entity *T, options ...gormrepository.Option) error {
	err := r.GormRepository.Create(ctx, entity, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) Save(ctx context.Context, entity *T, options ...gormrepository.Option) error {
	err := r.GormRepository.Save(ctx, entity, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) UpdateById(ctx context.Context, id uuid.UUID, entity *T, options ...gormrepository.Option) error {
	err := r.GormRepository.UpdateById(ctx, id, entity, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) UpdateByIdInPlace(ctx context.Context, id uuid.UUID, entity *T, updateFunc func(), options ...gormrepository.Option) error {
	err := r.GormRepository.UpdateByIdInPlace(ctx, id, entity, updateFunc, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) UpdateByIdWithMask(ctx context.Context, id uuid.UUID, mask map[string]interface{}, entity *T, options ...gormrepository.Option) error {
	err := r.GormRepository.UpdateByIdWithMask(ctx, id, mask, entity, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) UpdateByIdWithMap(ctx context.Context, id uuid.UUID, values map[string]interface{}, options ...gormrepository.Option) (*T, error) {
	result, err := r.GormRepository.UpdateByIdWithMap(ctx, id, values, options...)
	if err != nil {
		return result, err
	}

	// Handle cache invalidation for the updated entity
	if cacheErr := r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, result)
	}, options); cacheErr != nil {
		r.logDebug(fmt.Sprintf("Failed to handle cache invalidation after UpdateByIdWithMap: %v", cacheErr))
	}

	return result, nil
}

func (r *CachedGormRepository[T]) UpdateInPlace(ctx context.Context, entity *T, updateFunc func(), options ...gormrepository.Option) error {
	err := r.GormRepository.UpdateInPlace(ctx, entity, updateFunc, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) DeleteById(ctx context.Context, id uuid.UUID, options ...gormrepository.Option) error {
	err := r.GormRepository.DeleteById(ctx, id, options...)
	if err != nil {
		return err
	}

	idStr := id.String()
	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListAndId(ctx, idStr)
	}, options)
}

// Association methods
func (r *CachedGormRepository[T]) AppendAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...gormrepository.Option) error {
	err := r.GormRepository.AppendAssociation(ctx, entity, association, values, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) RemoveAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...gormrepository.Option) error {
	err := r.GormRepository.RemoveAssociation(ctx, entity, association, values, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

func (r *CachedGormRepository[T]) ReplaceAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...gormrepository.Option) error {
	err := r.GormRepository.ReplaceAssociation(ctx, entity, association, values, options...)
	if err != nil {
		return err
	}

	return r.handleCacheInvalidation(ctx, func(ctx context.Context) error {
		return r.forgetCacheListFromData(ctx, entity)
	}, options)
}

// BeginTransaction delegates to the underlying repository
func (r *CachedGormRepository[T]) BeginTransaction() *gormrepository.Tx {
	return r.GormRepository.BeginTransaction()
}

// Transaction-aware cache handling

// handleCacheInvalidation either queues cache operations for transaction commit or executes immediately
func (r *CachedGormRepository[T]) handleCacheInvalidation(ctx context.Context, operation func(context.Context) error, options []gormrepository.Option) error {
	// Apply options to get the potentially transaction-aware DB
	db := r.applyOptionsToGetDB(options)

	// Check for transaction context
	tx := gormrepository.GetTransactionFromDB(db)
	if tx != nil {
		tx.TransactionCacheInvalid = true
		// Queue the operation to be executed on commit
		tx.QueueCacheOperation(operation)
		return nil
	}

	// Execute immediately if not in transaction
	return operation(ctx)
}

// applyOptionsToGetDB applies options to get the DB instance that may contain transaction context
func (r *CachedGormRepository[T]) applyOptionsToGetDB(options []gormrepository.Option) *gorm.DB {
	db := r.GetDB()
	for _, option := range options {
		if option != nil {
			db = option(db)
		}
	}
	return db
}

// Cache invalidation helpers

func (r *CachedGormRepository[T]) forgetCacheListFromData(ctx context.Context, data interface{}) error {
	tags := r.makeListKeyFromData(data)
	tags = append(tags, r.makeKey("no-account:list"))

	rawTags := make([]RawTag, len(tags))
	for i, tag := range tags {
		rawTags[i] = tag
	}

	return r.cache.ForgetByTags(ctx, rawTags)
}

// func (r *CachedGormRepository[T]) forgetCacheById(ctx context.Context, id string) error {
// 	tags := []RawTag{r.makeKey(id)}
// 	return r.cache.ForgetByTags(ctx, tags)
// }

// func (r *CachedGormRepository[T]) forgetCacheList(ctx context.Context) error {
// 	tags := []RawTag{r.makeKey("no-account:list")}
// 	return r.cache.ForgetByTags(ctx, tags)
// }

func (r *CachedGormRepository[T]) forgetCacheListAndId(ctx context.Context, id string) error {
	tags := []RawTag{
		r.makeKey(id),
		r.makeKey("no-account:list"),
	}
	return r.cache.ForgetByTags(ctx, tags)
}

// Debug logging helper
func (r *CachedGormRepository[T]) logDebug(message string) {
	if r.debugEnabled {
		fmt.Printf("[CachedGormRepository] %s\n", message)
	}
}

func (r *CachedGormRepository[T]) optionsToQuery(options []gormrepository.Option) map[string]interface{} {
	query := make(map[string]interface{})

	// Crie um DB "seco" só para aplicar as opções
	tempDB := r.GetDB() //.Session(&gorm.Session{DryRun: true})

	for _, option := range options {
		if option != nil {
			tempDB = option(tempDB)
		}
	}

	// Em vez de Find, apenas obtenha Statement
	stmt := &gorm.Statement{DB: tempDB}
	_ = stmt.Parse(new(T)) // parse apenas o schema do model

	// Extraia apenas os componentes relevantes
	// Preloads (relations)
	if len(tempDB.Statement.Preloads) > 0 {
		preloads := make([]string, 0, len(tempDB.Statement.Preloads))
		for key := range tempDB.Statement.Preloads {
			preloads = append(preloads, key)
		}
		query["preloads"] = preloads
	}

	// Joins (relations)
	if len(tempDB.Statement.Joins) > 0 {
		joins := make([]string, len(tempDB.Statement.Joins))
		for i, join := range tempDB.Statement.Joins {
			joins[i] = join.Name
		}
		query["joins"] = joins
	}

	// Selects (campos selecionados)
	if len(tempDB.Statement.Selects) > 0 {
		query["selects"] = tempDB.Statement.Selects
	}

	// Omits (campos omitidos)
	if len(tempDB.Statement.Omits) > 0 {
		query["omits"] = tempDB.Statement.Omits
	}

	// Table name
	if tempDB.Statement.Table != "" {
		query["table"] = tempDB.Statement.Table
	}

	// Vars (binds)
	if len(tempDB.Statement.Vars) > 0 {
		vars := make([]string, len(tempDB.Statement.Vars))
		for i, v := range tempDB.Statement.Vars {
			vars[i] = fmt.Sprintf("%v", v)
		}
		query["vars"] = vars
	}

	// Adicione um hash simples das opções para fallback
	if len(options) > 0 {
		query["options_count"] = len(options)
	}

	return query
}

func newEntity[T any]() T {
	var entity T
	entityType := reflect.TypeOf(entity)
	if entityType.Kind() == reflect.Ptr {
		return reflect.New(entityType.Elem()).Interface().(T)
	}
	return entity
}
