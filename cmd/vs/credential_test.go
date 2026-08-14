package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theSecret is deliberately long and distinctive so a leak into stdout, stderr
// or the vault file is unambiguous when a test greps for it.
const theSecret = "sk-cli-test-1234567890-abcdefghijklmnop"

// TestCredentialSetThenStatusThenRemove walks the whole lifecycle the way a user
// would, against a real encrypted vault on disk.
func TestCredentialSetThenStatusThenRemove(t *testing.T) {
	dir, configPath := newCLIWorkspace(t)
	vaultPath := filepath.Join(dir, "credentials.vault")

	stdout, _, err := runCredentialCLI(t, theSecret, "set", "-config", configPath, "openai")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(stdout, "provider/openai") || !strings.Contains(stdout, "vault") {
		t.Fatalf("set should report the key and the backend, got %q", stdout)
	}

	stdout, _, err = runCredentialCLI(t, "", "status", "-config", configPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stdout, "openai") || !strings.Contains(stdout, "vault") {
		t.Fatalf("status should show openai backed by the vault, got %q", stdout)
	}

	stdout, _, err = runCredentialCLI(t, "", "rm", "-config", configPath, "openai")
	if err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(stdout, "removed") {
		t.Fatalf("rm should confirm the removal, got %q", stdout)
	}

	stdout, _, err = runCredentialCLI(t, "", "status", "-config", configPath)
	if err != nil {
		t.Fatalf("status after rm: %v", err)
	}
	if !strings.Contains(stdout, "missing") {
		t.Fatalf("status should report the key as missing after rm, got %q", stdout)
	}

	if _, err := os.Stat(vaultPath); err != nil {
		t.Fatalf("the vault file should still exist after rm: %v", err)
	}
}

// TestCredentialOutputNeverContainsTheSecret is the acceptance check: no command
// may echo the key back, on either stream.
func TestCredentialOutputNeverContainsTheSecret(t *testing.T) {
	_, configPath := newCLIWorkspace(t)

	runs := []struct {
		name  string
		stdin string
		args  []string
	}{
		{"set", theSecret, []string{"set", "-config", configPath, "openai"}},
		{"status", "", []string{"status", "-config", configPath}},
		{"status one", "", []string{"status", "-config", configPath, "openai"}},
		{"rm", "", []string{"rm", "-config", configPath, "openai"}},
	}

	for _, run := range runs {
		stdout, stderr, err := runCredentialCLI(t, run.stdin, run.args...)
		if err != nil {
			t.Fatalf("%s: %v", run.name, err)
		}
		if strings.Contains(stdout, theSecret) {
			t.Errorf("%s leaked the key on stdout: %q", run.name, stdout)
		}
		if strings.Contains(stderr, theSecret) {
			t.Errorf("%s leaked the key on stderr: %q", run.name, stderr)
		}
	}
}

// TestCredentialSetLeavesNoPlaintextOnDisk guards the storage layer through the
// CLI, which is the path users actually take.
func TestCredentialSetLeavesNoPlaintextOnDisk(t *testing.T) {
	dir, configPath := newCLIWorkspace(t)

	if _, _, err := runCredentialCLI(t, theSecret, "set", "-config", configPath, "openai"); err != nil {
		t.Fatalf("set: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "credentials.vault"))
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	if strings.Contains(string(raw), theSecret) {
		t.Fatalf("the vault file holds the key in plaintext:\n%s", raw)
	}
}

// TestCredentialSetRejectsAnEmptyKey keeps an accidental empty pipe from
// overwriting a working credential with nothing.
func TestCredentialSetRejectsAnEmptyKey(t *testing.T) {
	_, configPath := newCLIWorkspace(t)

	_, _, err := runCredentialCLI(t, "\n", "set", "-config", configPath, "openai")
	if err == nil {
		t.Fatal("expected an error for an empty key")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("the error should say the key was empty, got %v", err)
	}
}

// TestCredentialSetWarnsOnAnUnknownProvider catches the typo case: the write
// succeeds, but the user is told nothing will read it.
func TestCredentialSetWarnsOnAnUnknownProvider(t *testing.T) {
	_, configPath := newCLIWorkspace(t)

	_, stderr, err := runCredentialCLI(t, theSecret, "set", "-config", configPath, "opeanai")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(stderr, "opeanai") || !strings.Contains(stderr, "warning") {
		t.Fatalf("expected a warning about the unknown provider, got %q", stderr)
	}
}

// TestCredentialRemoveExplainsEnvironmentBackedKeys checks the confusing case:
// rm cannot touch a variable exported by the shell, so it must say so instead of
// claiming the key does not exist.
func TestCredentialRemoveExplainsEnvironmentBackedKeys(t *testing.T) {
	_, configPath := newCLIWorkspace(t)
	t.Setenv("OPENAI_API_KEY", theSecret)

	_, _, err := runCredentialCLI(t, "", "rm", "-config", configPath, "openai")
	if err == nil {
		t.Fatal("expected an error: vs cannot unset a variable in the user's shell")
	}
	if !strings.Contains(err.Error(), "env") {
		t.Fatalf("the error should name the env backend, got %v", err)
	}
	if strings.Contains(err.Error(), theSecret) {
		t.Fatalf("the error leaked the key: %v", err)
	}
}

// TestCredentialSetWarnsWhenTheEnvironmentShadowsTheNewKey covers the support
// question this warning exists to pre-empt: "I set my key and nothing changed".
func TestCredentialSetWarnsWhenTheEnvironmentShadowsTheNewKey(t *testing.T) {
	_, configPath := newCLIWorkspace(t)
	t.Setenv("OPENAI_API_KEY", "sk-an-older-key-still-exported")

	_, stderr, err := runCredentialCLI(t, theSecret, "set", "-config", configPath, "openai")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(stderr, "precedence") {
		t.Fatalf("expected a shadowing warning, got %q", stderr)
	}
}

// TestCredentialStatusReportsALockedVault stops a locked vault from reading as
// "you have no credentials".
func TestCredentialStatusReportsALockedVault(t *testing.T) {
	_, configPath := newCLIWorkspace(t)
	t.Setenv(vaultPassphraseEnv, "")

	_, stderr, err := runCredentialCLI(t, "", "status", "-config", configPath)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stderr, "locked") {
		t.Fatalf("expected a note about the locked vault, got %q", stderr)
	}
}

func TestCredentialRejectsUnknownSubcommands(t *testing.T) {
	if _, _, err := runCredentialCLI(t, "", "rotate"); err == nil {
		t.Fatal("expected an error for an unknown subcommand")
	}
}

// newCLIWorkspace writes a config pinned to the vault backend, so the test does
// not touch the developer's real keychain.
func newCLIWorkspace(t *testing.T) (dir, configPath string) {
	t.Helper()

	dir = t.TempDir()
	configPath = filepath.Join(dir, "config.yaml")
	body := "credentials:\n" +
		"  backend: vault\n" +
		"  vault_path: " + filepath.Join(dir, "credentials.vault") + "\n" +
		"providers:\n" +
		"  - name: openai\n" +
		"    base_url: https://api.openai.com/v1\n" +
		"    model: gpt-4o-mini\n"
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// The env store sits first in every chain, so leave nothing there unless a
	// test puts it there on purpose.
	t.Setenv("VS_CREDENTIAL_PROVIDER_OPENAI", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv(vaultPassphraseEnv, "test-vault-passphrase")

	return dir, configPath
}

// runCredentialCLI invokes the subcommand with stdin, stdout and stderr backed by
// files, which keeps the code under test unaware that it is being tested and
// makes IsTerminal report false, exercising the piped path.
func runCredentialCLI(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	dir := t.TempDir()
	in := writeStream(t, filepath.Join(dir, "stdin"), stdin)
	out := createStream(t, filepath.Join(dir, "stdout"))
	errStream := createStream(t, filepath.Join(dir, "stderr"))

	originalIn, originalOut, originalErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = in, out, errStream
	err = allCommands().run(context.Background(), "credential", args)
	os.Stdin, os.Stdout, os.Stderr = originalIn, originalOut, originalErr

	in.Close()
	out.Close()
	errStream.Close()

	return readStream(t, filepath.Join(dir, "stdout")), readStream(t, filepath.Join(dir, "stderr")), err
}

func writeStream(t *testing.T, path, body string) *os.File {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return f
}

func createStream(t *testing.T, path string) *os.File {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	return f
}

func readStream(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
