package credential

// This is an internal test because it exercises the unexported per-platform
// keychain backend directly. Going through Open would silently fall back to the
// vault on a machine without a keychain, and a test that passes by testing
// something else is worse than one that skips.

import (
	"context"
	"errors"
	"testing"
)

const keychainSecret = "keychain-integration-secret-7d2a"

// TestKeychainRoundTrip is the integration test named in the acceptance
// criteria. It runs for real on a developer machine with a keychain, and skips
// with a stated reason where none exists — on CI, or on a Linux host without
// secret-tool installed. The three platform implementations share this one test
// because they share one contract.
func TestKeychainRoundTrip(t *testing.T) {
	if !keychainAvailable() {
		t.Skip("no OS keychain on this machine: the auto chain falls back to the encrypted vault")
	}

	ctx := context.Background()
	store := newKeychainStore()

	// A test-specific key so a failure cannot disturb a real credential.
	key := "test/" + t.Name()

	// The keychain outlives the process, so an interrupted earlier run must
	// not make this one fail.
	_ = store.Delete(ctx, key)
	t.Cleanup(func() { _ = store.Delete(ctx, key) })

	if err := store.Set(ctx, key, keychainSecret); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != keychainSecret {
		t.Fatalf("got %q, want %q", got, keychainSecret)
	}

	// Set must update in place rather than fail on a duplicate entry.
	const updated = "keychain-integration-secret-updated-9e14"
	if err := store.Set(ctx, key, updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, err := store.Get(ctx, key); err != nil || got != updated {
		t.Fatalf("after update got %q, %v", got, err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete got %v, want ErrNotFound", err)
	}
}

func TestKeychainMissingKeyIsNotFound(t *testing.T) {
	if !keychainAvailable() {
		t.Skip("no OS keychain on this machine")
	}

	_, err := newKeychainStore().Get(context.Background(), "test/definitely-absent-"+t.Name())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
