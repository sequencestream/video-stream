package credential

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// EnvStore reads credentials from environment variables.
//
// It sits first in every chain. Containers and CI runners have no keychain, and
// putting the keychain first would make every lookup there pay for an IPC round
// trip that is guaranteed to fail. Keeping the environment first also means this
// change is backwards compatible with deployments that already export their keys.
type EnvStore struct {
	// lookup is injectable so tests do not have to mutate the real environment.
	lookup func(string) (string, bool)
}

var _ Store = (*EnvStore)(nil)

// NewEnvStore returns a store backed by the process environment.
func NewEnvStore() *EnvStore { return &EnvStore{lookup: os.LookupEnv} }

// Name identifies this backend.
func (s *EnvStore) Name() string { return "env" }

// Get reads the variable derived from key. A key of "provider/openai" maps to
// VS_CREDENTIAL_PROVIDER_OPENAI, and the conventional OPENAI_API_KEY is also
// accepted so users who already exported the vendor's standard variable do not
// have to rename it.
func (s *EnvStore) Get(_ context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}

	for _, name := range s.envNames(key) {
		if v, ok := s.lookup(name); ok && v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("%w: no environment variable for %q", ErrNotFound, key)
}

// Set always fails: this process cannot export a variable into the user's shell.
func (s *EnvStore) Set(context.Context, string, string) error {
	return fmt.Errorf("%w: set the variable in your shell instead", ErrReadOnly)
}

// Delete always fails, for the same reason as Set.
func (s *EnvStore) Delete(context.Context, string) error {
	return fmt.Errorf("%w: unset the variable in your shell instead", ErrReadOnly)
}

// envNames lists the variables consulted for a key, most specific first.
func (s *EnvStore) envNames(key string) []string {
	names := []string{"VS_CREDENTIAL_" + envSuffix(key)}

	// provider/openai -> OPENAI_API_KEY, the name the vendor's own docs use.
	if name, ok := strings.CutPrefix(key, "provider/"); ok && name != "" {
		names = append(names, envSuffix(name)+"_API_KEY")
	}
	return names
}

// envSuffix converts a credential key into the uppercase, underscore-separated
// form environment variables use.
func envSuffix(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range strings.ToUpper(key) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
