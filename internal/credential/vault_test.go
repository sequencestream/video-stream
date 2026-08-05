package credential_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sequencestream/video-stream/internal/credential"
)

// vaultSecret avoids the "sk-" shape the repository's secret scanner looks for.
const vaultSecret = "vault-round-trip-secret-4c81"

func newVault(t *testing.T, passphrase string) (*credential.VaultStore, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "credentials.vault")
	return credential.NewVaultStore(path, passphrase), path
}

func TestVaultRoundTrip(t *testing.T) {
	ctx := context.Background()
	vault, _ := newVault(t, "correct horse battery staple")

	key := credential.ProviderKey("openai")
	if err := vault.Set(ctx, key, vaultSecret); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := vault.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != vaultSecret {
		t.Fatalf("got %q, want %q", got, vaultSecret)
	}
}

// A second store instance must be able to read what the first wrote, otherwise
// the vault only works within a single process lifetime.
func TestVaultPersistsAcrossInstances(t *testing.T) {
	ctx := context.Background()
	const passphrase = "correct horse battery staple"

	first, path := newVault(t, passphrase)
	key := credential.ProviderKey("openai")
	if err := first.Set(ctx, key, vaultSecret); err != nil {
		t.Fatalf("set: %v", err)
	}

	second := credential.NewVaultStore(path, passphrase)
	got, err := second.Get(ctx, key)
	if err != nil {
		t.Fatalf("get from a fresh instance: %v", err)
	}
	if got != vaultSecret {
		t.Fatalf("got %q, want %q", got, vaultSecret)
	}
}

// The point of the vault is that the file itself reveals nothing, so this reads
// the raw bytes rather than trusting the API.
func TestVaultFileLeaksNeitherSecretNorKeyNames(t *testing.T) {
	ctx := context.Background()
	vault, path := newVault(t, "correct horse battery staple")

	if err := vault.Set(ctx, credential.ProviderKey("openai"), vaultSecret); err != nil {
		t.Fatalf("set: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vault file: %v", err)
	}

	if bytes.Contains(raw, []byte(vaultSecret)) {
		t.Error("the secret appears in the vault file in plaintext")
	}
	// Encrypting the whole table rather than per entry is what keeps the key
	// names — and hence which providers are configured — out of the clear.
	if bytes.Contains(raw, []byte("provider/openai")) {
		t.Error("the credential key name appears in the vault file in plaintext")
	}
}

func TestVaultFileIsOwnerOnly(t *testing.T) {
	ctx := context.Background()
	vault, path := newVault(t, "correct horse battery staple")

	if err := vault.Set(ctx, credential.ProviderKey("openai"), vaultSecret); err != nil {
		t.Fatalf("set: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat vault: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("vault permissions are %o, want 600", perm)
	}
}

// A wrong passphrase must be reported as such. Conflating it with "not found"
// would send the user off to re-issue an API key they already stored.
func TestVaultWrongPassphraseIsDistinctFromMissing(t *testing.T) {
	ctx := context.Background()
	const passphrase = "correct horse battery staple"

	vault, path := newVault(t, passphrase)
	key := credential.ProviderKey("openai")
	if err := vault.Set(ctx, key, vaultSecret); err != nil {
		t.Fatalf("set: %v", err)
	}

	wrong := credential.NewVaultStore(path, "not the passphrase")
	_, err := wrong.Get(ctx, key)
	if !errors.Is(err, credential.ErrBadPassphrase) {
		t.Fatalf("got %v, want ErrBadPassphrase", err)
	}
}

func TestVaultMissingFileIsNotFound(t *testing.T) {
	ctx := context.Background()
	vault, _ := newVault(t, "correct horse battery staple")

	_, err := vault.Get(ctx, credential.ProviderKey("openai"))
	if !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestVaultWithoutPassphraseIsLocked(t *testing.T) {
	ctx := context.Background()
	vault, _ := newVault(t, "")

	if vault.Unlocked() {
		t.Error("a vault without a passphrase must report itself locked")
	}

	_, err := vault.Get(ctx, credential.ProviderKey("openai"))
	if !errors.Is(err, credential.ErrLocked) {
		t.Fatalf("got %v, want ErrLocked", err)
	}
	if err := vault.Set(ctx, credential.ProviderKey("openai"), vaultSecret); !errors.Is(err, credential.ErrLocked) {
		t.Fatalf("set got %v, want ErrLocked", err)
	}
}

func TestVaultDelete(t *testing.T) {
	ctx := context.Background()
	vault, _ := newVault(t, "correct horse battery staple")

	key := credential.ProviderKey("openai")
	if err := vault.Set(ctx, key, vaultSecret); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := vault.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := vault.Get(ctx, key); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("got %v after delete, want ErrNotFound", err)
	}
	if err := vault.Delete(ctx, key); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("second delete got %v, want ErrNotFound", err)
	}
}

// Storing a second credential must not destroy the first: save rewrites the
// whole file, so a read-modify-write bug here would be silent data loss.
func TestVaultKeepsExistingEntriesOnWrite(t *testing.T) {
	ctx := context.Background()
	vault, _ := newVault(t, "correct horse battery staple")

	if err := vault.Set(ctx, credential.ProviderKey("openai"), vaultSecret); err != nil {
		t.Fatalf("set first: %v", err)
	}
	if err := vault.Set(ctx, credential.ProviderKey("anthropic"), "second-provider-secret-b72e"); err != nil {
		t.Fatalf("set second: %v", err)
	}

	if got, err := vault.Get(ctx, credential.ProviderKey("openai")); err != nil || got != vaultSecret {
		t.Fatalf("first credential lost: got %q, err %v", got, err)
	}
}

// A secret containing a newline would be truncated by the keychain backends
// that pipe it over stdin, so it is rejected everywhere for consistency.
func TestVaultRejectsUnusableSecrets(t *testing.T) {
	ctx := context.Background()
	vault, _ := newVault(t, "correct horse battery staple")

	for name, secret := range map[string]string{
		"empty":   "",
		"newline": "line-one\nline-two",
	} {
		t.Run(name, func(t *testing.T) {
			if err := vault.Set(ctx, credential.ProviderKey("openai"), secret); err == nil {
				t.Fatal("expected the write to be rejected")
			}
		})
	}
}
