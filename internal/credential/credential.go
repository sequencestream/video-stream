// Package credential keeps model and platform secrets on the user's machine.
//
// Secrets never travel to a server we run. They live in the operating system's
// own keychain (macOS Keychain, Windows Credential Manager, Linux Secret
// Service) or, when no keychain is available, in a passphrase-encrypted vault
// file next to the task database. This is a deliberate product stance rather
// than a missing feature: the cost is that moving to a new machine means
// re-entering credentials, and we accept it so that a leak of our
// infrastructure can never expose a user's keys.
//
// A Store is asked for a secret at the moment it is needed and the result is
// used and dropped. Nothing here caches a decrypted secret in a long-lived
// structure, which is the first line of defence against leaks; package redact
// is the second.
package credential

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrNotFound means the backend worked but holds no such credential.
	ErrNotFound = errors.New("credential not found")
	// ErrReadOnly means the backend cannot store credentials. The environment
	// backend returns this: it can read VS_* variables but not set them for
	// anyone else.
	ErrReadOnly = errors.New("credential store is read-only")
	// ErrUnavailable means the backend is not usable on this machine, e.g.
	// secret-tool is not installed on a Linux host.
	ErrUnavailable = errors.New("credential store is unavailable")
	// ErrLocked means an encrypted vault exists but no passphrase was supplied.
	ErrLocked = errors.New("credential vault is locked: no passphrase supplied")
	// ErrBadPassphrase means the vault decrypted to garbage, which for an AEAD
	// can only mean the wrong passphrase. It is kept distinct from ErrNotFound
	// so the user is told to fix their passphrase rather than re-enter a key
	// they already stored.
	ErrBadPassphrase = errors.New("credential vault passphrase is incorrect")
)

// Store reads and writes secrets on the local machine.
//
// There is deliberately no List method. Enumerating credentials differs sharply
// across the three platforms — on macOS it prompts for authorisation, on Linux
// it requires a schema match — and we do not need it: the set of provider names
// is already in the configuration, so callers probe with Get instead.
type Store interface {
	// Name identifies the backend in receipts and diagnostics, e.g. "keychain".
	Name() string
	// Get returns the secret for key, or ErrNotFound.
	Get(ctx context.Context, key string) (string, error)
	// Set stores the secret, or returns ErrReadOnly.
	Set(ctx context.Context, key, secret string) error
	// Delete removes the secret. Deleting a missing key returns ErrNotFound.
	Delete(ctx context.Context, key string) error
}

// service is the keychain service (or credential target) all entries share, so
// video-stream's credentials are distinguishable from every other application's
// in the user's keychain.
const service = "video-stream"

// ProviderKey is the credential key for a model provider.
//
// Keys are namespaced by kind so that a later intent storing platform cookies
// under "platform/<name>" cannot collide with a provider that happens to share
// a name, without needing a schema change.
func ProviderKey(provider string) string {
	return "provider/" + strings.TrimSpace(provider)
}

// validateKey rejects keys that would be ambiguous or awkward once passed to a
// keychain CLI. Keys are constructed internally, so this guards against a
// caller mistake rather than untrusted input.
func validateKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("credential key must not be empty")
	}
	if strings.ContainsAny(key, "\n\r\x00") {
		return fmt.Errorf("credential key must not contain control characters")
	}
	return nil
}

// validateSecret rejects secrets that cannot survive a round trip. Backends
// that shuttle secrets over pipes treat a newline as a terminator, so a secret
// containing one would be silently truncated — failing loudly is better.
func validateSecret(secret string) error {
	if secret == "" {
		return fmt.Errorf("secret must not be empty")
	}
	if strings.ContainsAny(secret, "\n\r\x00") {
		return fmt.Errorf("secret must not contain newlines or null bytes")
	}
	return nil
}
