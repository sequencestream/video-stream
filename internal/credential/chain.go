package credential

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Backend names accepted by Options.Backend.
const (
	// BackendAuto tries the environment, then the OS keychain, then the
	// encrypted vault. This is the default.
	BackendAuto = "auto"
	// BackendKeychain requires the OS keychain and fails at startup if it is
	// missing, rather than silently degrading.
	BackendKeychain = "keychain"
	// BackendVault uses the encrypted file vault.
	BackendVault = "vault"
	// BackendEnv reads environment variables only, reproducing the behaviour
	// this project had before credentials had a home.
	BackendEnv = "env"
)

// Options configures Open.
type Options struct {
	// Backend selects the chain. Empty means BackendAuto.
	Backend string
	// VaultPath is where the encrypted vault lives.
	VaultPath string
	// VaultPassphrase unlocks the vault. Empty leaves it locked.
	VaultPassphrase string
}

// Chain tries several stores in order.
//
// Reads walk the chain and take the first hit. Writes go to the first store
// that can accept them, which skips the read-only environment store.
type Chain struct {
	stores []Store
}

var _ Store = (*Chain)(nil)

// NewChain builds a chain from stores, in priority order.
func NewChain(stores ...Store) *Chain { return &Chain{stores: stores} }

// Name lists the backends in order, e.g. "env+keychain", so receipts and logs
// show what was actually consulted.
func (c *Chain) Name() string {
	names := make([]string, 0, len(c.stores))
	for _, s := range c.stores {
		names = append(names, s.Name())
	}
	return strings.Join(names, "+")
}

// Stores exposes the members so callers can report which one answered.
func (c *Chain) Stores() []Store { return c.stores }

// Get returns the first hit. A backend that is merely unavailable or locked is
// skipped rather than failing the whole lookup: a machine without a keychain
// should still be able to read a credential from the environment.
func (c *Chain) Get(ctx context.Context, key string) (string, error) {
	for _, store := range c.stores {
		secret, err := store.Get(ctx, key)
		if err == nil {
			return secret, nil
		}
		if isSkippable(err) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("%w: %q in %s", ErrNotFound, key, c.Name())
}

// Source returns the name of the backend holding key, without returning the
// secret. Diagnostics use this to answer "where is this credential coming
// from?", which is the only genuinely useful thing to show when a user reports
// that a key they set is not taking effect.
func (c *Chain) Source(ctx context.Context, key string) (string, bool) {
	for _, store := range c.stores {
		if _, err := store.Get(ctx, key); err == nil {
			return store.Name(), true
		}
	}
	return "", false
}

// Set writes to the first store that accepts writes.
func (c *Chain) Set(ctx context.Context, key, secret string) error {
	_, err := c.SetIn(ctx, key, secret)
	return err
}

// SetIn writes to the first store that accepts writes and reports its name, so
// a caller can tell the user where the secret actually landed. Set is the Store
// interface form that discards the name.
func (c *Chain) SetIn(ctx context.Context, key, secret string) (string, error) {
	for _, store := range c.stores {
		err := store.Set(ctx, key, secret)
		if err == nil {
			return store.Name(), nil
		}
		if errors.Is(err, ErrReadOnly) || errors.Is(err, ErrUnavailable) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("no writable credential store in %s", c.Name())
}

// Delete removes key from every store that holds it, so a credential is not
// left shadowed in a lower-priority backend after the user believes they
// removed it. Absence everywhere is reported as ErrNotFound.
func (c *Chain) Delete(ctx context.Context, key string) error {
	deleted := false
	for _, store := range c.stores {
		err := store.Delete(ctx, key)
		switch {
		case err == nil:
			deleted = true
		case isSkippable(err), errors.Is(err, ErrReadOnly):
			continue
		default:
			return err
		}
	}

	if !deleted {
		return fmt.Errorf("%w: %q in %s", ErrNotFound, key, c.Name())
	}
	return nil
}

// isSkippable reports whether an error means "this backend has nothing to say"
// rather than "this lookup failed".
func isSkippable(err error) bool {
	return errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrLocked)
}

// Open builds the credential chain described by opts.
func Open(opts Options) (*Chain, error) {
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" {
		backend = BackendAuto
	}

	env := NewEnvStore()

	switch backend {
	case BackendEnv:
		return NewChain(env), nil

	case BackendKeychain:
		// The user asked for the keychain explicitly, so a missing one is an
		// error rather than a cue to degrade quietly.
		if !keychainAvailable() {
			return nil, fmt.Errorf("%w: credentials.backend is %q but no OS keychain was found", ErrUnavailable, backend)
		}
		return NewChain(env, newKeychainStore()), nil

	case BackendVault:
		if opts.VaultPath == "" {
			return nil, fmt.Errorf("credentials.backend is %q but no vault path was configured", backend)
		}
		return NewChain(env, NewVaultStore(opts.VaultPath, opts.VaultPassphrase)), nil

	case BackendAuto:
		stores := []Store{env}
		if keychainAvailable() {
			stores = append(stores, newKeychainStore())
		}
		if opts.VaultPath != "" {
			stores = append(stores, NewVaultStore(opts.VaultPath, opts.VaultPassphrase))
		}
		return NewChain(stores...), nil

	default:
		return nil, fmt.Errorf("unknown credentials.backend %q: want auto, keychain, vault or env", opts.Backend)
	}
}
