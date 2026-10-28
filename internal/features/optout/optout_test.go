package optout

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type fakeRepository struct {
	entries map[string]bool
}

func key(sessionID uuid.UUID, recipient string) string { return sessionID.String() + "/" + recipient }

func (f *fakeRepository) Add(_ context.Context, id uuid.UUID, r string) error {
	f.entries[key(id, r)] = true
	return nil
}
func (f *fakeRepository) Remove(_ context.Context, id uuid.UUID, r string) error {
	delete(f.entries, key(id, r))
	return nil
}
func (f *fakeRepository) Exists(_ context.Context, id uuid.UUID, r string) (bool, error) {
	return f.entries[key(id, r)], nil
}
func (f *fakeRepository) List(context.Context, uuid.UUID) ([]Entry, error) { return nil, nil }

type fakeOwnership struct{ owner, session uuid.UUID }

func (f fakeOwnership) Owns(_ context.Context, tenantID, sessionID uuid.UUID) error {
	if tenantID != f.owner || sessionID != f.session {
		return ErrSessionNotFound
	}
	return nil
}

func TestOptOutIsPerSessionAndNormalized(t *testing.T) {
	tenantID, sessionID, otherSession := uuid.New(), uuid.New(), uuid.New()
	service := NewService(&fakeRepository{entries: map[string]bool{}}, fakeOwnership{tenantID, sessionID})
	ctx := context.Background()

	if err := service.Add(ctx, tenantID, sessionID, "+51 936-416-623"); err != nil {
		t.Fatal(err)
	}
	if out, _ := service.IsOptedOut(ctx, sessionID, "51936416623"); !out {
		t.Error("the recipient must be opted out regardless of formatting")
	}
	if out, _ := service.IsOptedOut(ctx, otherSession, "51936416623"); out {
		t.Error("an opt-out applies only to the session it was registered on")
	}

	if err := service.Remove(ctx, tenantID, sessionID, "51936416623"); err != nil {
		t.Fatal(err)
	}
	if out, _ := service.IsOptedOut(ctx, sessionID, "51936416623"); out {
		t.Error("the recipient should be subscribed again after Remove")
	}
}

func TestOptOutRequiresSessionOwnershipAndValidPhone(t *testing.T) {
	tenantID, sessionID := uuid.New(), uuid.New()
	service := NewService(&fakeRepository{entries: map[string]bool{}}, fakeOwnership{tenantID, sessionID})
	ctx := context.Background()

	if err := service.Add(ctx, uuid.New(), sessionID, "51936416623"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("another tenant must not manage the session's opt-outs, got %v", err)
	}
	if err := service.Add(ctx, tenantID, sessionID, "not a phone"); !errors.Is(err, ErrInvalidRecipient) {
		t.Errorf("expected ErrInvalidRecipient, got %v", err)
	}
}
