package credential

import (
	"context"
	"fmt"
	"maps"
	"sync"
)

// MemoryStore keeps credentials in memory. It exists so tests and callers that
// need a stub do not have to touch the user's real keychain, mirroring the
// telemetry package's MemoryReporter.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]string
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store, optionally seeded.
func NewMemoryStore(seed map[string]string) *MemoryStore {
	entries := make(map[string]string, len(seed))
	maps.Copy(entries, seed)
	return &MemoryStore{entries: entries}
}

// Name identifies this backend.
func (s *MemoryStore) Name() string { return "memory" }

// Get returns the stored secret or ErrNotFound.
func (s *MemoryStore) Get(_ context.Context, key string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	secret, ok := s.entries[key]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	return secret, nil
}

// Set stores the secret.
func (s *MemoryStore) Set(_ context.Context, key, secret string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateSecret(secret); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = secret
	return nil
}

// Delete removes the secret.
func (s *MemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.entries[key]; !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	delete(s.entries, key)
	return nil
}
