package memorycontext

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)

func validateText(name, value string) error {
	const maxBytes = 256
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be nonempty UTF-8 of at most %d bytes: %w", name, maxBytes, ErrInvalid)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control characters: %w", name, ErrInvalid)
		}
	}
	return nil
}

func validateIdentity(owner, id string) error {
	if err := validateText("owner", owner); err != nil {
		return err
	}
	if !identifier.MatchString(id) {
		return fmt.Errorf("invalid context ID: %w", ErrInvalid)
	}
	return nil
}

func validateMutation(owner, key string) error {
	if err := validateText("owner", owner); err != nil {
		return err
	}
	return validateText("idempotency key", key)
}

func validatePins(pins []Pin) error {
	if len(pins) > MaxPins {
		return fmt.Errorf("at most %d pins: %w", MaxPins, ErrInvalid)
	}
	seen := make(map[string]struct{}, len(pins))
	for _, pin := range pins {
		if !identifier.MatchString(pin.DocumentID) {
			return fmt.Errorf("invalid pinned document ID: %w", ErrInvalid)
		}
		if err := validateText("pinned version", pin.Version); err != nil {
			return err
		}
		if _, ok := seen[pin.DocumentID]; ok {
			return fmt.Errorf("duplicate pinned document: %w", ErrInvalid)
		}
		seen[pin.DocumentID] = struct{}{}
	}
	return nil
}

func normalizeSearch(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(raw) > MaxPayloadBytes || !json.Valid(raw) {
		return nil, fmt.Errorf("invalid search JSON: %w", ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("search settings must be a JSON object: %w", ErrInvalid)
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("encode search settings: %w", err)
	}
	return encoded, nil
}

func normalizeExpiry(expiry Expiry) (Expiry, error) {
	if !expiry.Set && expiry.At != nil {
		return Expiry{}, fmt.Errorf("expiry At requires Set: %w", ErrInvalid)
	}
	if expiry.At != nil {
		if expiry.At.Year() < 1970 || expiry.At.Year() > 9999 {
			return Expiry{}, fmt.Errorf("expiry outside range: %w", ErrInvalid)
		}
		at := expiry.At.UTC().Truncate(time.Millisecond)
		expiry.At = &at
	}
	return expiry, nil
}

func boundedJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode context payload: %w", err)
	}
	if len(encoded) > MaxPayloadBytes {
		return nil, fmt.Errorf("context payload exceeds %d bytes: %w", MaxPayloadBytes, ErrInvalid)
	}
	return encoded, nil
}

func fingerprint(operation string, value any) (string, error) {
	encoded, err := boundedJSON(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(operation+"\n"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

func opaqueID() string { return rand.Text() }

//nolint:gocritic // A value copy isolates normalization from the caller; referenced data is cloned below.
func normalizeCreate(request CreateRequest) (CreateRequest, error) {
	if err := validateMutation(request.Owner, request.IdempotencyKey); err != nil {
		return request, err
	}
	fields := map[string]string{"name": request.Name, "collection": request.Collection, "generation": request.Generation}
	for name, value := range fields {
		if err := validateText(name, value); err != nil {
			return request, err
		}
	}
	if err := validatePins(request.Pins); err != nil {
		return request, err
	}
	var err error
	request.Search, err = normalizeSearch(request.Search)
	if err != nil {
		return request, err
	}
	request.Pins = append([]Pin{}, request.Pins...)
	request.Expiry, err = normalizeExpiry(request.Expiry)
	return request, err
}

//nolint:gocritic // A value copy isolates normalization from the caller; referenced data is cloned below.
func normalizeUpdate(request UpdateRequest) (UpdateRequest, error) {
	if err := validateMutation(request.Owner, request.IdempotencyKey); err != nil {
		return request, err
	}
	if err := validateIdentity(request.Owner, request.ID); err != nil {
		return request, err
	}
	if err := validateText("expected version", request.ExpectedVersion); err != nil {
		return request, err
	}
	if request.Name != nil {
		if err := validateText("name", *request.Name); err != nil {
			return request, err
		}
		name := *request.Name
		request.Name = &name
	}
	if request.Pins != nil {
		if err := validatePins(*request.Pins); err != nil {
			return request, err
		}
		pins := append([]Pin{}, (*request.Pins)...)
		request.Pins = &pins
	}
	if request.Search != nil {
		search, err := normalizeSearch(*request.Search)
		if err != nil {
			return request, err
		}
		request.Search = &search
	}
	var err error
	request.Expiry, err = normalizeExpiry(request.Expiry)
	return request, err
}
