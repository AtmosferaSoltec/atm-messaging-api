package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type fakeRepository struct {
	byHash map[string]Tenant
}

func newFakeRepository() *fakeRepository { return &fakeRepository{byHash: map[string]Tenant{}} }

// nameTaken mimics the case-insensitive unique index on tenant names.
func (f *fakeRepository) nameTaken(name string, except uuid.UUID) bool {
	for _, t := range f.byHash {
		if t.ID != except && strings.EqualFold(strings.TrimSpace(t.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func (f *fakeRepository) Create(_ context.Context, t Tenant, hash string) error {
	if f.nameTaken(t.Name, uuid.Nil) {
		return ErrNameTaken
	}
	f.byHash[hash] = t
	return nil
}

func (f *fakeRepository) FindByAPIKeyHash(_ context.Context, hash string) (Tenant, error) {
	t, ok := f.byHash[hash]
	if !ok {
		return Tenant{}, ErrNotFound
	}
	return t, nil
}

func (f *fakeRepository) FindByID(_ context.Context, id uuid.UUID) (Tenant, error) {
	for _, t := range f.byHash {
		if t.ID == id {
			return t, nil
		}
	}
	return Tenant{}, ErrNotFound
}

func (f *fakeRepository) List(context.Context) ([]Tenant, error) {
	tenants := []Tenant{}
	for _, t := range f.byHash {
		tenants = append(tenants, t)
	}
	return tenants, nil
}

func (f *fakeRepository) Update(_ context.Context, id uuid.UUID, name *string, isActive *bool) (Tenant, error) {
	for hash, t := range f.byHash {
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
			f.byHash[hash] = t
			return t, nil
		}
	}
	return Tenant{}, ErrNotFound
}

func (f *fakeRepository) UpdateWebhookURL(_ context.Context, id uuid.UUID, url string) error {
	for hash, t := range f.byHash {
		if t.ID == id {
			t.WebhookURL = url
			f.byHash[hash] = t
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeRepository) ReplaceAPIKey(_ context.Context, id uuid.UUID, prefix, hash string) error {
	for oldHash, t := range f.byHash {
		if t.ID == id {
			delete(f.byHash, oldHash)
			t.APIKeyPrefix = prefix
			f.byHash[hash] = t
			return nil
		}
	}
	return ErrNotFound
}

func TestGenerateAPIKey(t *testing.T) {
	key, prefix, hash, err := generateAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "atm_") || !strings.HasPrefix(key, prefix) {
		t.Errorf("unexpected key format: prefix %q", prefix)
	}
	if hash != HashAPIKey(key) || strings.Contains(hash, key) {
		t.Error("hash must be the digest of the key and not contain it")
	}
	other, _, _, _ := generateAPIKey()
	if other == key {
		t.Error("keys must be random")
	}
}

func TestServiceCreateStoresOnlyTheHash(t *testing.T) {
	repo := newFakeRepository()
	service := NewService(repo)

	created, err := service.Create(context.Background(), "  La Vendimia ")
	if err != nil {
		t.Fatal(err)
	}
	if created.Tenant.Name != "La Vendimia" {
		t.Errorf("name should be trimmed, got %q", created.Tenant.Name)
	}
	if _, stored := repo.byHash[created.APIKey]; stored {
		t.Error("the clear API key must never be stored")
	}
	if _, stored := repo.byHash[HashAPIKey(created.APIKey)]; !stored {
		t.Error("the key hash must be stored")
	}
}

func TestServiceCreateRejectsInvalidName(t *testing.T) {
	service := NewService(newFakeRepository())
	for _, name := range []string{"", "   ", strings.Repeat("x", 101)} {
		if _, err := service.Create(context.Background(), name); err != ErrInvalidName {
			t.Errorf("name %q: expected ErrInvalidName, got %v", name, err)
		}
	}
}

func TestServiceAuthenticate(t *testing.T) {
	repo := newFakeRepository()
	service := NewService(repo)
	created, _ := service.Create(context.Background(), "Tenant")

	got, err := service.Authenticate(context.Background(), created.APIKey)
	if err != nil || got.ID != created.Tenant.ID {
		t.Fatalf("valid key should authenticate, got %v / %v", got, err)
	}

	for _, key := range []string{"", "atm_unknown"} {
		if _, err := service.Authenticate(context.Background(), key); err != ErrInvalidAPIKey {
			t.Errorf("key %q: expected ErrInvalidAPIKey, got %v", key, err)
		}
	}

	inactive := repo.byHash[HashAPIKey(created.APIKey)]
	inactive.IsActive = false
	repo.byHash[HashAPIKey(created.APIKey)] = inactive
	if _, err := service.Authenticate(context.Background(), created.APIKey); err != ErrInvalidAPIKey {
		t.Errorf("inactive tenant must not authenticate, got %v", err)
	}
}

func TestAuthMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := NewService(newFakeRepository())
	created, _ := service.Create(context.Background(), "Tenant")

	router := gin.New()
	router.GET("/protected", AuthMiddleware(service), func(c *gin.Context) {
		c.String(http.StatusOK, FromContext(c).ID.String())
	})

	cases := []struct {
		name   string
		header string
		status int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + created.APIKey, http.StatusUnauthorized},
		{"unknown key", "Bearer atm_unknown", http.StatusUnauthorized},
		{"valid key", "Bearer " + created.APIKey, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("expected %d, got %d", tc.status, rec.Code)
			}
			if tc.status == http.StatusOK && rec.Body.String() != created.Tenant.ID.String() {
				t.Error("middleware must expose the tenant derived from the key")
			}
		})
	}
	_ = uuid.Nil
}

func TestRotateAPIKeyInvalidatesTheOldKey(t *testing.T) {
	service := NewService(newFakeRepository())
	created, _ := service.Create(context.Background(), "Tenant")

	rotated, err := service.RotateAPIKey(context.Background(), created.Tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.APIKey == created.APIKey {
		t.Fatal("rotation must issue a different key")
	}
	if _, err := service.Authenticate(context.Background(), created.APIKey); err != ErrInvalidAPIKey {
		t.Errorf("the old key must stop working, got %v", err)
	}
	if got, err := service.Authenticate(context.Background(), rotated.APIKey); err != nil || got.ID != created.Tenant.ID {
		t.Errorf("the new key must authenticate the same tenant, got %v / %v", got, err)
	}
}

func TestUpdateTenant(t *testing.T) {
	service := NewService(newFakeRepository())
	created, _ := service.Create(context.Background(), "Tenant")

	inactive := false
	updated, err := service.Update(context.Background(), created.Tenant.ID, nil, &inactive)
	if err != nil || updated.IsActive {
		t.Fatalf("expected the tenant to be deactivated, got %v / %v", updated, err)
	}
	if _, err := service.Authenticate(context.Background(), created.APIKey); err != ErrInvalidAPIKey {
		t.Errorf("a deactivated tenant's key must stop working, got %v", err)
	}

	blank := "   "
	if _, err := service.Update(context.Background(), created.Tenant.ID, &blank, nil); err != ErrInvalidName {
		t.Errorf("expected ErrInvalidName, got %v", err)
	}
}

func TestTenantNamesAreUniqueIgnoringCaseAndSpaces(t *testing.T) {
	service := NewService(newFakeRepository())
	ctx := context.Background()

	first, err := service.Create(ctx, "La Vendimia")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"La Vendimia", "la vendimia", "  LA VENDIMIA  "} {
		if _, err := service.Create(ctx, name); err != ErrNameTaken {
			t.Errorf("%q: expected ErrNameTaken, got %v", name, err)
		}
	}

	other, err := service.Create(ctx, "Iris")
	if err != nil {
		t.Fatal(err)
	}
	taken := "la vendimia"
	if _, err := service.Update(ctx, other.Tenant.ID, &taken, nil); err != ErrNameTaken {
		t.Errorf("renaming onto an existing name must fail, got %v", err)
	}
	same := "LA VENDIMIA"
	if _, err := service.Update(ctx, first.Tenant.ID, &same, nil); err != nil {
		t.Errorf("a tenant may change the case of its own name: %v", err)
	}
}
