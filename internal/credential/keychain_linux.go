//go:build linux

package credential

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// secretToolBin is libsecret's official CLI and the standard way to reach the
// Secret Service D-Bus API (GNOME Keyring, KWallet's Secret Service bridge).
// Shelling out to it keeps us free of both cgo and a D-Bus client dependency.
const secretToolBin = "secret-tool"

// keychainStore talks to the Secret Service via secret-tool.
type keychainStore struct{}

var _ Store = (*keychainStore)(nil)

func newKeychainStore() Store { return &keychainStore{} }

func (s *keychainStore) Name() string { return "keychain" }

// keychainAvailable reports whether secret-tool is installed. It frequently is
// not on servers and minimal desktops, in which case the chain falls through to
// the encrypted vault rather than failing.
func keychainAvailable() bool {
	_, err := exec.LookPath(secretToolBin)
	return err == nil
}

// attributes identify an entry. secret-tool matches on the full attribute set,
// so the same pair must be used for lookup, store and clear.
func attributes(key string) []string {
	return []string{"service", service, "account", key}
}

func (s *keychainStore) Get(ctx context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}

	args := append([]string{"lookup"}, attributes(key)...)
	cmd := exec.CommandContext(ctx, secretToolBin, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// secret-tool exits non-zero with empty output for a miss and also
		// for real errors, so stderr is what distinguishes them.
		if stderr.Len() == 0 {
			return "", fmt.Errorf("%w: %q is not in the secret service", ErrNotFound, key)
		}
		return "", fmt.Errorf("secret service lookup for %q: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}

	// lookup does not add a trailing newline, but trimming is harmless and
	// guards against a future change in the tool.
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func (s *keychainStore) Set(ctx context.Context, key, secret string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateSecret(secret); err != nil {
		return err
	}

	// store reads the secret from stdin, so it never reaches argv where ps
	// would expose it to every process on the machine.
	args := append([]string{"store", "--label", service + ": " + key}, attributes(key)...)
	cmd := exec.CommandContext(ctx, secretToolBin, args...)
	cmd.Stdin = strings.NewReader(secret)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret service write for %q: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s *keychainStore) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}

	// clear succeeds even when nothing matched, so absence is checked first
	// to keep Delete's contract identical across platforms.
	if _, err := s.Get(ctx, key); err != nil {
		return err
	}

	args := append([]string{"clear"}, attributes(key)...)
	cmd := exec.CommandContext(ctx, secretToolBin, args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret service delete for %q: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
