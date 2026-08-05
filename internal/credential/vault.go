package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Vault file format constants.
const (
	vaultVersion = 1
	vaultKDF     = "pbkdf2-hmac-sha256"
	// vaultIterations is written into every file so it can be raised later
	// without stranding vaults written by older builds.
	vaultIterations = 600_000
	saltLength      = 32
	keyLength       = 32 // AES-256
	// vaultFileMode keeps the vault readable only by its owner. The contents
	// are encrypted anyway, but there is no reason to hand an attacker the
	// ciphertext and let them attack the passphrase offline at leisure.
	vaultFileMode = 0o600
)

// envelope is the on-disk structure. Only the parameters needed to derive the
// key are plaintext; everything about which credentials exist is inside the
// ciphertext.
type envelope struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Salt       []byte `json:"salt"`
	Iterations int    `json:"iterations"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// VaultStore is the fallback backend: an AES-256-GCM encrypted file unlocked by
// a user passphrase.
//
// The whole entry table is encrypted as one blob rather than per-entry. Encrypting
// entries individually would leave the key names — and therefore which providers
// the user has configured — readable in the clear.
type VaultStore struct {
	path       string
	passphrase string

	// mu serialises read-modify-write cycles. Two concurrent Sets would
	// otherwise each decrypt the same snapshot and the later write would
	// silently discard the earlier credential.
	mu sync.Mutex
}

var _ Store = (*VaultStore)(nil)

// NewVaultStore returns a vault stored at path. An empty passphrase is allowed:
// the store then reports ErrLocked on use rather than refusing to construct, so
// the daemon can start and report which providers are unavailable instead of
// dying because one optional credential could not be read.
func NewVaultStore(path, passphrase string) *VaultStore {
	return &VaultStore{path: path, passphrase: passphrase}
}

// Name identifies this backend.
func (s *VaultStore) Name() string { return "vault" }

// Path is the vault file location, used by diagnostics and documentation.
func (s *VaultStore) Path() string { return s.path }

// Unlocked reports whether a passphrase is available.
func (s *VaultStore) Unlocked() bool { return s.passphrase != "" }

// Get decrypts the vault and returns one entry.
func (s *VaultStore) Get(_ context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load()
	if err != nil {
		return "", err
	}

	secret, ok := entries[key]
	if !ok {
		return "", fmt.Errorf("%w: %q is not in the vault", ErrNotFound, key)
	}
	return secret, nil
}

// Set writes one entry, creating the vault if it does not exist.
func (s *VaultStore) Set(_ context.Context, key, secret string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateSecret(secret); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load()
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if entries == nil {
		entries = make(map[string]string)
	}

	entries[key] = secret
	return s.save(entries)
}

// Delete removes one entry.
func (s *VaultStore) Delete(_ context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := entries[key]; !ok {
		return fmt.Errorf("%w: %q is not in the vault", ErrNotFound, key)
	}

	delete(entries, key)
	return s.save(entries)
}

// load reads and decrypts the vault. A missing file yields ErrNotFound so
// callers can distinguish "no vault yet" from "wrong passphrase".
func (s *VaultStore) load() (map[string]string, error) {
	if s.passphrase == "" {
		return nil, ErrLocked
	}

	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: no vault at %s", ErrNotFound, s.path)
	}
	if err != nil {
		return nil, fmt.Errorf("read vault: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("vault at %s is corrupt: %w", s.path, err)
	}
	if env.Version != vaultVersion {
		return nil, fmt.Errorf("vault at %s has unsupported version %d", s.path, env.Version)
	}
	if env.KDF != vaultKDF {
		return nil, fmt.Errorf("vault at %s uses unsupported kdf %q", s.path, env.KDF)
	}

	aead, err := s.aead(env.Salt, env.Iterations)
	if err != nil {
		return nil, err
	}

	plaintext, err := aead.Open(nil, env.Nonce, env.Ciphertext, nil)
	if err != nil {
		// GCM authentication covers both the key and the ciphertext. A
		// failure here effectively means the wrong passphrase, and saying
		// "maybe the file is damaged" would only send the user looking in
		// the wrong place.
		return nil, ErrBadPassphrase
	}

	var entries map[string]string
	if err := json.Unmarshal(plaintext, &entries); err != nil {
		return nil, fmt.Errorf("vault at %s decrypted to invalid content: %w", s.path, err)
	}
	return entries, nil
}

// save encrypts entries under a freshly derived key and replaces the file
// atomically. A fresh salt and nonce per write avoids ever reusing a
// (key, nonce) pair, which would break GCM's guarantees.
func (s *VaultStore) save(entries map[string]string) error {
	if s.passphrase == "" {
		return ErrLocked
	}

	plaintext, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode vault entries: %w", err)
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate vault salt: %w", err)
	}

	aead, err := s.aead(salt, vaultIterations)
	if err != nil {
		return err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generate vault nonce: %w", err)
	}

	data, err := json.Marshal(envelope{
		Version:    vaultVersion,
		KDF:        vaultKDF,
		Salt:       salt,
		Iterations: vaultIterations,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, nil),
	})
	if err != nil {
		return fmt.Errorf("encode vault envelope: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create vault directory: %w", err)
	}
	return writeFileAtomic(s.path, data, vaultFileMode)
}

// aead derives the key and builds the cipher.
func (s *VaultStore) aead(salt []byte, iterations int) (cipher.AEAD, error) {
	if iterations < 1 {
		return nil, fmt.Errorf("vault iteration count must be positive, got %d", iterations)
	}

	key, err := pbkdf2.Key(sha256.New, s.passphrase, salt, iterations, keyLength)
	if err != nil {
		return nil, fmt.Errorf("derive vault key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("build vault cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("build vault aead: %w", err)
	}
	return aead, nil
}

// writeFileAtomic writes via a temporary file and renames it into place, so an
// interrupted write cannot leave a half-written vault that locks the user out
// of every credential they own.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary vault file: %w", err)
	}
	tmpName := tmp.Name()

	// Remove the temporary file on any path that does not reach the rename.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("set vault permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write vault: %w", err)
	}
	// Durability matters more than speed here: losing a credential write to a
	// crash means the user has to go re-issue an API key.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync vault: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close vault: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace vault: %w", err)
	}
	return nil
}
