package tenant

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the Repository.
var (
	ErrNotFound        = errors.New("tenant not found")
	ErrNameTaken       = errors.New("a tenant with this name already exists")
	ErrSystemCodeTaken = errors.New("another tenant is already linked to this system")
)

const (
	uniqueViolation = "23505"
	nameUniqueIndex = "tenants_name_lower_key"
)

// isNameTaken reports whether err is the unique-name violation (names are
// compared ignoring case and surrounding spaces).
func isNameTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == nameUniqueIndex
}

// Repository stores tenants.
type Repository interface {
	// Create inserts the tenant unless one already has its system code, and reports
	// whether it did.
	Create(ctx context.Context, tenant Tenant) (bool, error)
	FindBySystemCode(ctx context.Context, code string) (Tenant, error)
	FindByID(ctx context.Context, id uuid.UUID) (Tenant, error)
	List(ctx context.Context) ([]Tenant, error)
	// Update changes the name and/or active flag; nil fields are left as they are.
	Update(ctx context.Context, id uuid.UUID, name *string, isActive *bool) (Tenant, error)
	UpdateWebhookURL(ctx context.Context, id uuid.UUID, url string) error
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Create(ctx context.Context, t Tenant) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO tenants (id, name, system_code, webhook_secret, is_active)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (system_code) DO NOTHING`,
		t.ID, t.Name, t.SystemCode, t.WebhookSecret, t.IsActive)
	if isNameTaken(err) {
		return false, ErrNameTaken
	}
	if err != nil {
		return false, fmt.Errorf("insert tenant: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

const selectTenant = `
	SELECT id, name, system_code, webhook_secret, COALESCE(webhook_url, ''), is_active, created_at
	FROM tenants`

func scanTenant(row pgx.Row) (Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.Name, &t.SystemCode, &t.WebhookSecret, &t.WebhookURL, &t.IsActive, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	if err != nil {
		return Tenant{}, fmt.Errorf("scan tenant: %w", err)
	}
	return t, nil
}

func (r *pgRepository) FindBySystemCode(ctx context.Context, code string) (Tenant, error) {
	return scanTenant(r.pool.QueryRow(ctx, selectTenant+` WHERE system_code = $1`, code))
}

func (r *pgRepository) FindByID(ctx context.Context, id uuid.UUID) (Tenant, error) {
	return scanTenant(r.pool.QueryRow(ctx, selectTenant+` WHERE id = $1`, id))
}

func (r *pgRepository) List(ctx context.Context) ([]Tenant, error) {
	rows, err := r.pool.Query(ctx, selectTenant+` ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	tenants := []Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tenants: %w", err)
	}
	return tenants, nil
}

func (r *pgRepository) Update(ctx context.Context, id uuid.UUID, name *string, isActive *bool) (Tenant, error) {
	t, err := scanTenant(r.pool.QueryRow(ctx, `
		UPDATE tenants SET name = COALESCE($2, name), is_active = COALESCE($3, is_active), updated_at = now()
		WHERE id = $1
		RETURNING id, name, system_code, webhook_secret, COALESCE(webhook_url, ''), is_active, created_at`,
		id, name, isActive))
	if isNameTaken(err) {
		return Tenant{}, ErrNameTaken
	}
	return t, err
}

func (r *pgRepository) UpdateWebhookURL(ctx context.Context, id uuid.UUID, url string) error {
	var value *string
	if url != "" {
		value = &url
	}
	tag, err := r.pool.Exec(ctx, `UPDATE tenants SET webhook_url = $2, updated_at = now() WHERE id = $1`, id, value)
	if err != nil {
		return fmt.Errorf("update webhook url: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
