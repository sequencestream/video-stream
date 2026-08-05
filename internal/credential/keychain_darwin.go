//go:build darwin

package credential

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/sequencestream/video-stream/internal/redact"
)

// securityBin is the system keychain CLI. Shelling out to it avoids cgo, which
// this repository cannot take on: the whole distribution story is a single
// static binary, and linking Security.framework would break cross-compilation.
const securityBin = "/usr/bin/security"

// keychainStore talks to the macOS Keychain via /usr/bin/security.
type keychainStore struct{}

var _ Store = (*keychainStore)(nil)

func newKeychainStore() Store { return &keychainStore{} }

func (s *keychainStore) Name() string { return "keychain" }

// keychainAvailable reports whether the CLI exists. It is missing in a
// stripped-down container image even on darwin.
func keychainAvailable() bool {
	_, err := exec.LookPath(securityBin)
	return err == nil
}

func (s *keychainStore) Get(ctx context.Context, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}

	// -w prints the password alone on stdout. The secret never appears in
	// argv, which matters because argv is visible to every process on the
	// machine via ps.
	cmd := exec.CommandContext(ctx, securityBin,
		"find-generic-password", "-s", service, "-a", key, "-w")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		// security exits 44 for "item not found"; anything else is a real
		// failure such as a denied authorisation prompt.
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return "", fmt.Errorf("%w: %q is not in the keychain", ErrNotFound, key)
		}
		return "", fmt.Errorf("keychain lookup for %q: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}

	// -w emits a trailing newline that is not part of the secret.
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

func (s *keychainStore) Set(ctx context.Context, key, secret string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if err := validateSecret(secret); err != nil {
		return err
	}

	// The secret must not be passed in argv, because argv is readable by every
	// process on the machine via ps. "add-generic-password -w" without a value
	// prompts, but it reads the prompt from the controlling terminal rather
	// than stdin, so piping to it simply hangs.
	//
	// Interactive mode ("security -i") is the way out: it reads whole commands
	// from stdin, so the secret travels down a pipe instead of the argument
	// list, and the exit status of the failing command is still propagated.
	// -U updates an existing item rather than failing as a duplicate.
	command := fmt.Sprintf("add-generic-password -s %s -a %s -U -w %s\n",
		quoteArg(service), quoteArg(key), quoteArg(secret))

	cmd := exec.CommandContext(ctx, securityBin, "-i")
	cmd.Stdin = strings.NewReader(command)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Interactive mode can echo the failing command back on stderr, and
		// that command contains the secret. Strip it explicitly rather than
		// relying on redact, which only knows secrets already registered with
		// it — this one may be brand new.
		detail := strings.ReplaceAll(strings.TrimSpace(stderr.String()), secret, redact.Placeholder)
		return fmt.Errorf("keychain write for %q: %w: %s", key, err, detail)
	}
	return nil
}

// quoteArg renders s as a double-quoted argument for security's interactive
// parser, which understands backslash escapes inside quotes.
func quoteArg(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)

	b.WriteByte('"')
	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func (s *keychainStore) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, securityBin,
		"delete-generic-password", "-s", service, "-a", key)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return fmt.Errorf("%w: %q is not in the keychain", ErrNotFound, key)
		}
		return fmt.Errorf("keychain delete for %q: %w: %s", key, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
