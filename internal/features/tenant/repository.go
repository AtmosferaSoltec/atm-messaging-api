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
	uniqueViolation   = "23505"
	nameUniqueIndex   = "tenants_name_lower_key"
	systemUniqueIndex = "tenants_system_code_key"
)

// isNameTaken reports whether err is the unique-name violation (names are
// compared ignoring case and surrounding spaces).
func isNameTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == nameUniqueIndex
}

// isSystemCodeTaken reports whether err is the unique system-code violation.
func isSystemCodeTaken(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == systemUniqueIndex
}

// Repository stores tenants.
type Repository interface {
	Create(ctx context.Context, tenant Tenant, apiKeyHash string) error
	FindByAPIKeyHash(ctx context.Context, apiKeyHash string) (Tenant, error)
	FindBySystemCode(ctx context.Context, code string) (Tenant, error)
	FindByID(ctx context.Context, id uuid.UUID) (Tenant, error)
	List(ctx context.Context) ([]Tenant, error)
	// Update changes the name and/or active flag; nil fields are left as they are.
	Update(ctx context.Context, id uuid.UUID, name *string, isActive *bool) (Tenant, error)
	UpdateWebhookURL(ctx context.Context, id uuid.UUID, url string) error
	// SetSystemCode links the tenant to a system of ATM-Auth-API; an empty code unlinks it.
	SetSystemCode(ctx context.Context, id uuid.UUID, code string) error
	// ReplaceAPIKey swaps the stored key hash, which invalidates the old key at once.
	ReplaceAPIKey(ctx context.Context, id uuid.UUID, prefix, hash string) error
}

type pgRepository struct {
	pool *pgxpool.Pool
}

// NewRepository returns a PostgreSQL-backed Repository.
func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{pool: pool}
}

func (r *pgRepository) Create(ctx context.Context, t Tenant, apiKeyHash string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO tenants (id, name, api_key_prefix, api_key_hash, webhook_secret, is_active)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, t.Name, t.APIKeyPrefix, apiKeyHash, t.WebhookSecret, t.IsActive)
	if isNameTaken(err) {
		return ErrNameTaken
	}
	if err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}

const selectTenant = `
	SELECT id, name, api_key_prefix, COALESCE(system_code, ''), webhook_secret, COALESCE(webhook_url, ''), is_active, created_at
	FROM tenants`

func scanTenant(row pgx.Row) (Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.Name, &t.APIKeyPrefix, &t.SystemCode, &t.WebhookSecret, &t.WebhookURL, &t.IsActive, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	if err != nil {
		return Tenant{}, fmt.Errorf("scan tenant: %w", err)
	}
	return t, nil
}

func (r *pgRepository) FindByAPIKeyHash(ctx context.Context, apiKeyHash string) (Tenant, error) {
	return scanTenant(r.pool.QueryRow(ctx, selectTenant+` WHERE api_key_hash = $1`, apiKeyHash))
}

func (r *pgRepository) FindBySystemCode(ctx context.Context, code string) (Tenant, error) {
	return scanTenant(r.pool.QueryRow(ctx, selectTenant+` WHERE system_code = $1`, code))
}

func (r *pgRepository) SetSystemCode(ctx context.Context, id uuid.UUID, code string) error {
	var value *string
	if code != "" {
		value = &code
	}
	tag, err := r.pool.Exec(ctx, `UPDATE tenants SET system_code = $2, updated_at = now() WHERE id = $1`, id, value)
	if isSystemCodeTaken(err) {
		return ErrSystemCodeTaken
	}
	if err != nil {
		return fmt.Errorf("set system code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
		RETURNING id, name, api_key_prefix, COALESCE(system_code, ''), webhook_secret, COALESCE(webhook_url, ''), is_active, created_at`,
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

func (r *pgRepository) ReplaceAPIKey(ctx context.Context, id uuid.UUID, prefix, hash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE tenants SET api_key_prefix = $2, api_key_hash = $3, updated_at = now() WHERE id = $1`, id, prefix, hash)
	if err != nil {
		return fmt.Errorf("replace api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
