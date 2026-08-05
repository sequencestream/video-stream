//go:build !darwin && !linux && !windows

package credential

import (
	"context"
	"fmt"
	"runtime"
)

// On platforms without an OS keychain integration the store reports itself
// unavailable, so Open's auto chain falls through to the encrypted vault. The
// type still exists so the rest of the package compiles unchanged.
type keychainStore struct{}

var _ Store = (*keychainStore)(nil)

func newKeychainStore() Store { return &keychainStore{} }

func (s *keychainStore) Name() string { return "keychain" }

func keychainAvailable() bool { return false }

func (s *keychainStore) Get(context.Context, string) (string, error) {
	return "", unsupported()
}

func (s *keychainStore) Set(context.Context, string, string) error {
	return unsupported()
}

func (s *keychainStore) Delete(context.Context, string) error {
	return unsupported()
}

func unsupported() error {
	return fmt.Errorf("%w: no keychain integration for %s", ErrUnavailable, runtime.GOOS)
}
