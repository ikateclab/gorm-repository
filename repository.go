package gormrepository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Option represents a functional option for configuring the repository methods.
type Option func(*gorm.DB) *gorm.DB

type PaginationResult[T any] struct {
	Data        []T   `json:"data"`
	Total       int64 `json:"total"`
	Limit       int   `json:"limit"`
	Offset      int   `json:"offset"`
	CurrentPage int   `json:"currentPage"`
	LastPage    int   `json:"lastPage"`
	From        int   `json:"from"`
	To          int   `json:"to"`
}

// Interface methods to avoid circular dependency with test helpers
func (p *PaginationResult[T]) GetTotal() int64 {
	return p.Total
}

func (p *PaginationResult[T]) GetCurrentPage() int {
	return p.CurrentPage
}

func (p *PaginationResult[T]) GetLimit() int {
	return p.Limit
}

func (p *PaginationResult[T]) GetLastPage() int {
	return p.LastPage
}

func (p *PaginationResult[T]) GetData() []T {
	return p.Data
}

// Diffable represents entities that can generate clones and diffs
type Diffable[T any] interface {
	Clone() T
	Diff(T) map[string]interface{}
}

type Repository[T any] interface {
	FindMany(ctx context.Context, options ...Option) ([]T, error)
	FindPaginated(ctx context.Context, page int, pageSize int, options ...Option) (*PaginationResult[T], error)
	FindById(ctx context.Context, id uuid.UUID, options ...Option) (T, error)
	FindOne(ctx context.Context, options ...Option) (T, error)
	Create(ctx context.Context, entity T, options ...Option) error
	Save(ctx context.Context, entity T, options ...Option) error
	UpdateById(ctx context.Context, id uuid.UUID, entity T, options ...Option) error
	UpdateByIdWithMask(ctx context.Context, id uuid.UUID, mask map[string]interface{}, entity T, options ...Option) error
	UpdateByIdWithMap(ctx context.Context, id uuid.UUID, values map[string]interface{}, options ...Option) (T, error)
	UpdateByIdInPlace(ctx context.Context, id uuid.UUID, entity T, updateFunc func(), options ...Option) error
	UpdateInPlace(ctx context.Context, entity T, updateFunc func(), options ...Option) error
	DeleteById(ctx context.Context, id uuid.UUID, options ...Option) error
	BeginTransaction() *Tx
	AppendAssociation(ctx context.Context, entity T, association string, values interface{}, options ...Option) error
	RemoveAssociation(ctx context.Context, entity T, association string, values interface{}, options ...Option) error
	ReplaceAssociation(ctx context.Context, entity T, association string, values interface{}, options ...Option) error
	GetDB() *gorm.DB
}
