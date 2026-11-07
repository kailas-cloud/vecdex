package memorycontext

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/rueidis"
)

// Service validates and manages durable contexts. The caller supplies a trusted
// owner identity and validates collection access and the search settings schema.
type Service struct {
	repo *valkeyRepository
	now  func() time.Time
}

// New uses the application's shared Valkey client. Service does not close it.
func New(client rueidis.Client) *Service {
	return &Service{repo: &valkeyRepository{client: client}, now: time.Now}
}

// Create stores one context and its idempotency receipt in the same native transaction.
//
//nolint:gocritic // Public request values are copied so normalization cannot mutate caller state.
func (s *Service) Create(ctx context.Context, request CreateRequest) (MutationResult, error) {
	return s.CreateValidated(ctx, request, nil)
}

// CreateValidated checks a collection binding only for a new write. The callback
// runs after receipt lookup, so an identical replay survives collection deletion.
// It may run more than once on WATCH contention and must have no side effects.
//
//nolint:gocritic // Public request values are copied so normalization cannot mutate caller state.
func (s *Service) CreateValidated(
	ctx context.Context, request CreateRequest, validate func(context.Context) error,
) (MutationResult, error) {
	request, err := normalizeCreate(request)
	if err != nil {
		return MutationResult{}, err
	}
	digest, err := fingerprint("create", request)
	if err != nil {
		return MutationResult{}, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	expires := request.Expiry.At
	if !request.Expiry.Set {
		at := now.Add(DefaultLifetime)
		expires = &at
	}
	value := Context{ID: opaqueID(), Version: opaqueID(), Owner: request.Owner, Name: request.Name,
		Collection: request.Collection, Generation: request.Generation, Search: request.Search,
		Pins: request.Pins, ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}
	change := mutation{owner: request.Owner, id: value.ID, key: request.IdempotencyKey,
		digest: digest, operation: "create", validate: validate, apply: func(_ Context) Context { return value }}
	return s.repo.mutate(ctx, &change, s.now)
}

// Get returns the current value without extending its absolute expiry.
func (s *Service) Get(ctx context.Context, owner, id string) (Context, error) {
	if err := validateIdentity(owner, id); err != nil {
		return Context{}, err
	}
	return readContext(ctx, s.repo.client, owner, id, s.now())
}

// Update atomically checks the original version and changes only provided fields.
//
//nolint:gocritic // Public request values are copied so normalization cannot mutate caller state.
func (s *Service) Update(ctx context.Context, request UpdateRequest) (MutationResult, error) {
	request, err := normalizeUpdate(request)
	if err != nil {
		return MutationResult{}, err
	}
	digest, err := fingerprint("update", request)
	if err != nil {
		return MutationResult{}, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	version := opaqueID()
	change := mutation{owner: request.Owner, id: request.ID, key: request.IdempotencyKey,
		digest: digest, operation: "update", expected: request.ExpectedVersion,
		apply: func(value Context) Context {
			if request.Name != nil {
				value.Name = *request.Name
			}
			if request.Search != nil {
				value.Search = *request.Search
			}
			if request.Pins != nil {
				value.Pins = *request.Pins
			}
			if request.Expiry.Set {
				value.ExpiresAt = request.Expiry.At
			}
			value.Version, value.UpdatedAt = version, now
			return value
		}}
	return s.repo.mutate(ctx, &change, s.now)
}

// Delete removes the context and catalog membership, leaving all receipts intact.
func (s *Service) Delete(ctx context.Context, request DeleteRequest) (MutationResult, error) {
	if err := validateMutation(request.Owner, request.IdempotencyKey); err != nil {
		return MutationResult{}, err
	}
	if err := validateIdentity(request.Owner, request.ID); err != nil {
		return MutationResult{}, err
	}
	if err := validateText("expected version", request.ExpectedVersion); err != nil {
		return MutationResult{}, err
	}
	digest, err := fingerprint("delete", request)
	if err != nil {
		return MutationResult{}, err
	}
	change := mutation{owner: request.Owner, id: request.ID, key: request.IdempotencyKey,
		digest: digest, operation: "delete", expected: request.ExpectedVersion,
		apply: func(value Context) Context { return value }}
	return s.repo.mutate(ctx, &change, s.now)
}

// List reads an owner-bound page and performs bounded expiry-index maintenance.
func (s *Service) List(ctx context.Context, request ListRequest) (Page, error) {
	if err := validateText("owner", request.Owner); err != nil {
		return Page{}, err
	}
	if request.Limit == 0 {
		request.Limit = 20
	}
	if request.Limit < 1 || request.Limit > 100 {
		return Page{}, fmt.Errorf("list limit must be 1..100: %w", ErrInvalid)
	}
	after, err := decodeCursor(request.Owner, request.Cursor)
	if err != nil {
		return Page{}, err
	}
	return s.repo.list(ctx, request.Owner, after, request.Limit, s.now())
}
