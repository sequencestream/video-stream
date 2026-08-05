package main

// vs credential manages provider API keys.
//
// Unlike every other vs subcommand, this one talks to the local credential
// store directly instead of going through the main service over HTTP. Two
// reasons: a secret should not travel over a socket only to be stored on the
// same machine, and the OS keychain that holds it belongs to the logged-in
// user rather than to the daemon's process.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/credential"
)

// vaultPassphraseEnv unlocks the encrypted vault without a prompt, for scripts
// and CI.
const vaultPassphraseEnv = "VS_VAULT_PASSPHRASE"

func cmdCredential(ctx context.Context, args []string) error {
	if len(args) == 0 {
		credentialUsage()
		return errors.New("usage: vs credential <set|status|rm> [provider]")
	}

	switch args[0] {
	case "set":
		return credentialSet(ctx, args[1:])
	case "status", "list", "ls":
		return credentialStatus(ctx, args[1:])
	case "rm", "remove", "delete":
		return credentialRemove(ctx, args[1:])
	case "help", "--help", "-h":
		credentialUsage()
		return nil
	default:
		credentialUsage()
		return fmt.Errorf("unknown credential subcommand %q", args[0])
	}
}

func credentialUsage() {
	// io.WriteString rather than fmt.Fprint: the examples below contain a %s
	// that belongs to printf(1), not to Go.
	io.WriteString(os.Stderr, `vs credential - manage provider API keys in the local credential store

Usage:
  vs credential set <provider>     read a key from stdin and store it
  vs credential status [provider]  show which backend holds a key
  vs credential rm <provider>      remove a key from every backend that has it

The key is never taken as an argument, so it cannot end up in the process list
or the shell history. Type it at the prompt, or pipe it in:

  vs credential set openai
  printf %s "$OPENAI_API_KEY" | vs credential set openai

Flags:
  -config string   path to the YAML config file (env VS_CONFIG)

Environment:
  VS_VAULT_PASSPHRASE   unlocks the encrypted vault without prompting
`)
}

func credentialSet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("credential set", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to the YAML config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	provider := strings.TrimSpace(fs.Arg(0))
	if provider == "" {
		return errors.New("usage: vs credential set <provider>")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if _, ok := cfg.Provider(provider); !ok {
		// Not fatal, because setting the key before editing config.yaml is a
		// reasonable order to work in. But silently succeeding on a typo would
		// leave the user staring at a provider that still reports no key.
		fmt.Fprintf(os.Stderr, "vs: warning: no provider named %q is configured; the key is stored but nothing reads it yet\n", provider)
	}

	secret, err := readSecret(fmt.Sprintf("API key for %s: ", provider))
	if err != nil {
		return err
	}
	if secret == "" {
		return errors.New("the key was empty; nothing was stored")
	}

	key := credential.ProviderKey(provider)
	var backend string
	var chain *credential.Chain
	err = withCredentials(cfg, func(c *credential.Chain) error {
		chain = c
		backend, err = c.SetIn(ctx, key, secret)
		return err
	})
	if err != nil {
		return fmt.Errorf("store %s: %w", key, err)
	}

	fmt.Printf("stored %s in %s\n", key, backend)

	// A leftover environment variable outranks what we just wrote. Saying so
	// now is far cheaper than the user debugging why their new key had no
	// effect.
	if source, ok := chain.Source(ctx, key); ok && source != backend {
		fmt.Fprintf(os.Stderr, "vs: warning: the %s backend still takes precedence for %s\n", source, key)
	}
	return nil
}

func credentialStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("credential status", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to the YAML config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	chain, err := openChain(cfg, os.Getenv(vaultPassphraseEnv))
	if err != nil {
		return err
	}

	names := providerNames(cfg, fs.Arg(0))
	if len(names) == 0 {
		fmt.Println("no providers are configured")
		return nil
	}

	fmt.Printf("backends   %s\n\n", chain.Name())

	width := len("PROVIDER")
	for _, name := range names {
		width = max(width, len(name))
	}
	fmt.Printf("%-*s  %s\n", width, "PROVIDER", "CREDENTIAL")
	for _, name := range names {
		source, ok := chain.Source(ctx, credential.ProviderKey(name))
		if !ok {
			source = "missing"
		}
		fmt.Printf("%-*s  %s\n", width, name, source)
	}

	// A locked vault turns "missing" into a guess, so do not let it pass as an
	// answer.
	if hasLockedVault(chain) {
		fmt.Fprintf(os.Stderr, "\nvs: note: the encrypted vault is locked, so keys stored there read as missing; set %s\n", vaultPassphraseEnv)
	}
	return nil
}

func credentialRemove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("credential rm", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfigPath(), "path to the YAML config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	provider := strings.TrimSpace(fs.Arg(0))
	if provider == "" {
		return errors.New("usage: vs credential rm <provider>")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	key := credential.ProviderKey(provider)
	var chain *credential.Chain
	err = withCredentials(cfg, func(c *credential.Chain) error {
		chain = c
		return c.Delete(ctx, key)
	})

	if errors.Is(err, credential.ErrNotFound) {
		// The key may still resolve from a read-only backend. Reporting "not
		// found" then would contradict what status just printed.
		if source, ok := chain.Source(ctx, key); ok {
			return fmt.Errorf("%s comes from the %s backend, which vs cannot modify; unset the variable instead", key, source)
		}
		return fmt.Errorf("no stored credential for %q", provider)
	}
	if err != nil {
		return fmt.Errorf("remove %s: %w", key, err)
	}

	fmt.Printf("removed %s\n", key)
	return nil
}

// providerNames returns the providers to report on: the requested one, or every
// configured provider.
func providerNames(cfg config.Config, requested string) []string {
	if name := strings.TrimSpace(requested); name != "" {
		return []string{name}
	}
	names := make([]string, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		names = append(names, p.Name)
	}
	return names
}

// withCredentials runs op against the configured chain. If a write fails only
// because the vault has no passphrase, and there is a terminal to ask on, it
// prompts once and retries. Asking lazily keeps the common keychain path
// silent: most users never see a vault prompt at all.
func withCredentials(cfg config.Config, op func(*credential.Chain) error) error {
	chain, err := openChain(cfg, os.Getenv(vaultPassphraseEnv))
	if err != nil {
		return err
	}

	err = op(chain)
	if !errors.Is(err, credential.ErrLocked) || !stdinIsTerminal() {
		return err
	}

	passphrase, promptErr := readSecret("Vault passphrase: ")
	if promptErr != nil {
		return promptErr
	}
	if passphrase == "" {
		return err
	}

	if chain, err = openChain(cfg, passphrase); err != nil {
		return err
	}
	return op(chain)
}

func openChain(cfg config.Config, passphrase string) (*credential.Chain, error) {
	return credential.Open(credential.Options{
		Backend:         cfg.Credentials.Backend,
		VaultPath:       cfg.VaultPath(),
		VaultPassphrase: passphrase,
	})
}

// hasLockedVault reports whether the chain contains a vault that could not be
// opened, which makes any "missing" verdict unreliable.
func hasLockedVault(chain *credential.Chain) bool {
	for _, store := range chain.Stores() {
		if vault, ok := store.(*credential.VaultStore); ok && !vault.Unlocked() {
			return true
		}
	}
	return false
}

func stdinIsTerminal() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// readSecret reads one secret without echoing it back. Piped input is consumed
// whole and stripped of a trailing newline, so both of these behave the same:
//
//	printf %s "$KEY" | vs credential set openai
//	echo "$KEY"      | vs credential set openai
func readSecret(prompt string) (string, error) {
	if stdinIsTerminal() {
		fmt.Fprint(os.Stderr, prompt)
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read secret from stdin: %w", err)
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

func defaultConfigPath() string {
	if v := os.Getenv("VS_CONFIG"); v != "" {
		return v
	}
	return "config.yaml"
}
