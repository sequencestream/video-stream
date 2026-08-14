package main

// vs credential manages provider API keys.
//
// It is the one command that touches something other than media files: the
// local credential store. Keys stay in the OS keychain or an encrypted vault
// belonging to the logged-in user, never in the config file, and never as a
// command-line argument where the process list would expose them.

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

func credentialCommand() *Command {
	return &Command{
		Name:    "credential",
		Aliases: []string{"cred"},
		Group:   groupSetup,
		Summary: "Store API keys for the commands that call a hosted model",
		Args:    "<set|status|rm> [provider]",
		NoInput: true,
		Long: `Reads and writes the local credential store: the OS keychain where one is
available, an encrypted vault otherwise, with environment variables taking
precedence over both.

The key is never taken as an argument, so it cannot end up in the process list
or in shell history. Type it at the prompt, or pipe it in.

Nothing in the editing commands needs a key — speech recognition and every
ffmpeg operation run locally. This exists for the providers configured in the
config file.`,
		Examples: []Example{
			{Command: "vs credential set openai", Note: "prompt for the key, store it in the keychain"},
			{Command: `printf %s "$OPENAI_API_KEY" | vs credential set openai`, Note: "pipe it in from a script"},
			{Command: "vs credential status", Note: "show which backend holds each provider's key"},
			{Command: "vs credential rm openai"},
		},
		Setup: func(fs *flag.FlagSet) {},
		Run:   runCredential,
	}
}

func runCredential(ctx context.Context, env *Env, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: vs credential <set|status|rm> [provider]\nRun `vs credential --help` for details.")
	}
	switch args[0] {
	case "set":
		return credentialSet(ctx, env, args[1:])
	case "status", "list", "ls":
		return credentialStatus(ctx, env, args[1:])
	case "rm", "remove", "delete":
		return credentialRemove(ctx, env, args[1:])
	default:
		return fmt.Errorf("unknown credential subcommand %q: want set, status or rm", args[0])
	}
}

func credentialSet(ctx context.Context, env *Env, args []string) error {
	provider := ""
	if len(args) > 0 {
		provider = strings.TrimSpace(args[0])
	}
	if provider == "" {
		return errors.New("usage: vs credential set <provider>")
	}
	cfg := env.Config
	if _, ok := cfg.Provider(provider); !ok {
		// Not fatal, because setting the key before editing the config is a
		// reasonable order to work in. But silently succeeding on a typo would
		// leave the user staring at a provider that still reports no key.
		fmt.Fprintf(env.Stderr, "vs: warning: no provider named %q is configured; the key is stored but nothing reads it yet\n", provider)
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

func credentialStatus(ctx context.Context, env *Env, args []string) error {
	cfg := env.Config
	chain, err := openChain(cfg, os.Getenv(vaultPassphraseEnv))
	if err != nil {
		return err
	}

	requested := ""
	if len(args) > 0 {
		requested = args[0]
	}
	names := providerNames(cfg, requested)
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

func credentialRemove(ctx context.Context, env *Env, args []string) error {
	provider := ""
	if len(args) > 0 {
		provider = strings.TrimSpace(args[0])
	}
	if provider == "" {
		return errors.New("usage: vs credential rm <provider>")
	}
	cfg := env.Config

	key := credential.ProviderKey(provider)
	var chain *credential.Chain
	err := withCredentials(cfg, func(c *credential.Chain) error {
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
