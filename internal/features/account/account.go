// Package account manages the businesses inside a tenant, identified by the
// ID they have in the tenant's own system (external_ref).
package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxExternalRefLength = 100

// ErrInvalidExternalRef is returned when an account reference is empty or too long.
var ErrInvalidExternalRef = errors.New("account_ref must be between 1 and 100 characters")

// Account is a business inside a tenant.
type Account struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	ExternalRef string
	CreatedAt   time.Time
}

// Repository stores accounts.
type Repository interface {
	GetOrCreate(ctx context.Context, tenantID uuid.UUID, externalRef string) (Account, error)
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) GetOrCreate(ctx context.Context, tenantID uuid.UUID, externalRef string) (Account, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Account{}, fmt.Errorf("generate account id: %w", err)
	}

	// The no-op update makes RETURNING yield the row on conflict as well.
	var a Account
	err = r.pool.QueryRow(ctx, `
		INSERT INTO accounts (id, tenant_id, external_ref) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, external_ref) DO UPDATE SET external_ref = EXCLUDED.external_ref
		RETURNING id, tenant_id, external_ref, created_at`,
		id, tenantID, externalRef).
		Scan(&a.ID, &a.TenantID, &a.ExternalRef, &a.CreatedAt)
	if err != nil {
		return Account{}, fmt.Errorf("get or create account: %w", err)
	}
	return a, nil
}

// Service holds the account business rules.
type Service struct {
	repo Repository
}

// NewService creates a Service.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// Resolve returns the tenant's account for a reference, creating it on first use.
func (s *Service) Resolve(ctx context.Context, tenantID uuid.UUID, externalRef string) (Account, error) {
	externalRef = strings.TrimSpace(externalRef)
	if externalRef == "" || len(externalRef) > maxExternalRefLength {
		return Account{}, ErrInvalidExternalRef
	}
	return s.repo.GetOrCreate(ctx, tenantID, externalRef)
}
