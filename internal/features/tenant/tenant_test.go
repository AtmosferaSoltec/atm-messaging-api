package tenant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type fakeRepository struct {
	bySystem map[string]Tenant
}

func newFakeRepository() *fakeRepository { return &fakeRepository{bySystem: map[string]Tenant{}} }

// nameTaken mimics the case-insensitive unique index on tenant names.
func (f *fakeRepository) nameTaken(name string, except uuid.UUID) bool {
	for _, t := range f.bySystem {
		if t.ID != except && strings.EqualFold(strings.TrimSpace(t.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func (f *fakeRepository) Create(_ context.Context, t Tenant) (bool, error) {
	if _, exists := f.bySystem[t.SystemCode]; exists {
		return false, nil
	}
	if f.nameTaken(t.Name, uuid.Nil) {
		return false, ErrNameTaken
	}
	f.bySystem[t.SystemCode] = t
	return true, nil
}

func (f *fakeRepository) FindBySystemCode(_ context.Context, code string) (Tenant, error) {
	t, ok := f.bySystem[code]
	if !ok {
		return Tenant{}, ErrNotFound
	}
	return t, nil
}

func (f *fakeRepository) FindByID(_ context.Context, id uuid.UUID) (Tenant, error) {
	for _, t := range f.bySystem {
		if t.ID == id {
			return t, nil
		}
	}
	return Tenant{}, ErrNotFound
}

func (f *fakeRepository) List(context.Context) ([]Tenant, error) {
	tenants := []Tenant{}
	for _, t := range f.bySystem {
		tenants = append(tenants, t)
	}
	return tenants, nil
}

func (f *fakeRepository) Update(_ context.Context, id uuid.UUID, name *string, isActive *bool) (Tenant, error) {
	for code, t := range f.bySystem {
		if t.ID == id {
			if name != nil {
				if f.nameTaken(*name, id) {
					return Tenant{}, ErrNameTaken
				}
				t.Name = *name
			}
			if isActive != nil {
				t.IsActive = *isActive
			}
			f.bySystem[code] = t
			return t, nil
		}
	}
	return Tenant{}, ErrNotFound
}

func (f *fakeRepository) UpdateWebhookURL(_ context.Context, id uuid.UUID, url string) error {
	for code, t := range f.bySystem {
		if t.ID == id {
			t.WebhookURL = url
			f.bySystem[code] = t
			return nil
		}
	}
	return ErrNotFound
}

func TestProvisionCreatesAnActiveTenantOnce(t *testing.T) {
	ctx := context.Background()
	service := NewService(newFakeRepository())

	first, err := service.Provision(ctx, "lavendimia")
	if err != nil {
		t.Fatal(err)
	}
	if !first.IsActive || first.SystemCode != "lavendimia" || first.Name != "lavendimia" || first.WebhookSecret == "" {
		t.Fatalf("unexpected tenant: %+v", first)
	}
	again, err := service.Provision(ctx, "lavendimia")
	if err != nil || again.ID != first.ID || again.WebhookSecret != first.WebhookSecret {
		t.Fatalf("a known system must return its tenant unchanged: %+v / %v", again, err)
	}
}

func TestProvisionRejectsInvalidSystemCodes(t *testing.T) {
	service := NewService(newFakeRepository())
	for _, code := range []string{"", "Olympus", "my sys", "1sys", "-x", strings.Repeat("a", 101)} {
		if _, err := service.Provision(context.Background(), code); !errors.Is(err, ErrInvalidSystemCode) {
			t.Errorf("code %q: expected ErrInvalidSystemCode, got %v", code, err)
		}
	}
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepository()
	service := NewService(repo)

	got, err := service.Authenticate(ctx, "hotel")
	if err != nil || got.SystemCode != "hotel" {
		t.Fatalf("a new system is provisioned and accepted, got %v / %v", got, err)
	}
	if _, err := service.Authenticate(ctx, "Not Valid"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("invalid code: expected ErrInvalidCredentials, got %v", err)
	}

	inactive := false
	if _, err := service.Update(ctx, got.ID, nil, &inactive); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, "hotel"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("an inactive tenant must be refused, got %v", err)
	}
	if len(repo.bySystem) != 1 {
		t.Errorf("refusing must not create more tenants, have %d", len(repo.bySystem))
	}
}

func TestUpdateTenant(t *testing.T) {
	ctx := context.Background()
	service := NewService(newFakeRepository())
	created, _ := service.Provision(ctx, "tenant")

	inactive := false
	updated, err := service.Update(ctx, created.ID, nil, &inactive)
	if err != nil || updated.IsActive {
		t.Fatalf("expected the tenant to be deactivated, got %v / %v", updated, err)
	}

	blank := "   "
	if _, err := service.Update(ctx, created.ID, &blank, nil); !errors.Is(err, ErrInvalidName) {
		t.Errorf("expected ErrInvalidName, got %v", err)
	}
}

func TestTenantNamesAreUniqueIgnoringCaseAndSpaces(t *testing.T) {
	ctx := context.Background()
	service := NewService(newFakeRepository())

	first, _ := service.Provision(ctx, "lavendimia")
	other, _ := service.Provision(ctx, "iris")

	taken := "  LAVENDIMIA "
	if _, err := service.Update(ctx, other.ID, &taken, nil); !errors.Is(err, ErrNameTaken) {
		t.Errorf("renaming onto an existing name must fail, got %v", err)
	}
	same := "LaVendimia"
	if _, err := service.Update(ctx, first.ID, &same, nil); err != nil {
		t.Errorf("a tenant may change the case of its own name: %v", err)
	}
}

func TestWebhookEndpoint(t *testing.T) {
	ctx := context.Background()
	service := NewService(newFakeRepository())
	created, _ := service.Provision(ctx, "tenant")

	if url, secret, err := service.WebhookEndpoint(ctx, created.ID); err != nil || url != "" || secret != created.WebhookSecret {
		t.Fatalf("unexpected endpoint: %q %q %v", url, secret, err)
	}
	if err := service.SetWebhookURL(ctx, created.ID, "https://example.com/hook"); err != nil {
		t.Fatal(err)
	}
	if url, _, _ := service.WebhookEndpoint(ctx, created.ID); url != "https://example.com/hook" {
		t.Errorf("url not stored: %q", url)
	}
	if _, _, err := service.WebhookEndpoint(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown tenant: expected ErrNotFound, got %v", err)
	}
}
