package credential_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sequencestream/video-stream/internal/credential"
)

func TestChainPrefersTheEarlierStore(t *testing.T) {
	ctx := context.Background()
	key := credential.ProviderKey("openai")

	first := credential.NewMemoryStore(map[string]string{key: "from-the-first-store"})
	second := credential.NewMemoryStore(map[string]string{key: "from-the-second-store"})

	got, err := credential.NewChain(first, second).Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "from-the-first-store" {
		t.Fatalf("got %q, want the first store's value", got)
	}
}

func TestChainFallsThroughToTheNextStore(t *testing.T) {
	ctx := context.Background()
	key := credential.ProviderKey("openai")

	empty := credential.NewMemoryStore(nil)
	filled := credential.NewMemoryStore(map[string]string{key: "from-the-second-store"})

	got, err := credential.NewChain(empty, filled).Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "from-the-second-store" {
		t.Fatalf("got %q, want the second store's value", got)
	}
}

func TestChainReportsWhichStoreAnswered(t *testing.T) {
	ctx := context.Background()
	key := credential.ProviderKey("openai")

	chain := credential.NewChain(
		credential.NewMemoryStore(nil),
		credential.NewVaultStore(filepath.Join(t.TempDir(), "v"), ""), // locked, must be skipped
		credential.NewMemoryStore(map[string]string{key: "answer"}),
	)

	source, ok := chain.Source(ctx, key)
	if !ok {
		t.Fatal("expected the credential to be found")
	}
	if source != "memory" {
		t.Fatalf("got source %q, want memory", source)
	}

	if _, ok := chain.Source(ctx, credential.ProviderKey("absent")); ok {
		t.Error("a missing credential must not report a source")
	}
}

// A locked vault or an unavailable keychain must not abort the whole lookup,
// otherwise a machine without a keychain could not read credentials from the
// environment either.
func TestChainSkipsLockedAndUnavailableStores(t *testing.T) {
	ctx := context.Background()
	key := credential.ProviderKey("openai")

	chain := credential.NewChain(
		credential.NewVaultStore(filepath.Join(t.TempDir(), "vault"), ""),
		credential.NewMemoryStore(map[string]string{key: "reachable"}),
	)

	got, err := chain.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != "reachable" {
		t.Fatalf("got %q, want reachable", got)
	}
}

// Writes must skip the read-only environment store rather than failing on it.
func TestChainWritesToTheFirstWritableStore(t *testing.T) {
	ctx := context.Background()
	key := credential.ProviderKey("openai")

	writable := credential.NewMemoryStore(nil)
	chain := credential.NewChain(credential.NewEnvStore(), writable)

	if err := chain.Set(ctx, key, "written-through-the-chain"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, err := writable.Get(ctx, key); err != nil || got != "written-through-the-chain" {
		t.Fatalf("the writable store did not receive the secret: %q, %v", got, err)
	}
}

func TestEnvStoreRejectsWrites(t *testing.T) {
	ctx := context.Background()
	env := credential.NewEnvStore()

	if err := env.Set(ctx, credential.ProviderKey("openai"), "x"); !errors.Is(err, credential.ErrReadOnly) {
		t.Fatalf("set got %v, want ErrReadOnly", err)
	}
	if err := env.Delete(ctx, credential.ProviderKey("openai")); !errors.Is(err, credential.ErrReadOnly) {
		t.Fatalf("delete got %v, want ErrReadOnly", err)
	}
}

// Both the namespaced variable and the vendor's conventional name must work:
// users who already exported OPENAI_API_KEY should not have to rename it.
func TestEnvStoreReadsBothVariableNames(t *testing.T) {
	ctx := context.Background()
	key := credential.ProviderKey("openai")

	t.Run("namespaced", func(t *testing.T) {
		t.Setenv("VS_CREDENTIAL_PROVIDER_OPENAI", "namespaced-value")
		got, err := credential.NewEnvStore().Get(ctx, key)
		if err != nil || got != "namespaced-value" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("vendor convention", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "vendor-value")
		got, err := credential.NewEnvStore().Get(ctx, key)
		if err != nil || got != "vendor-value" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("namespaced wins", func(t *testing.T) {
		t.Setenv("VS_CREDENTIAL_PROVIDER_OPENAI", "namespaced-value")
		t.Setenv("OPENAI_API_KEY", "vendor-value")
		got, err := credential.NewEnvStore().Get(ctx, key)
		if err != nil || got != "namespaced-value" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
}

// Deleting must clear every backend, or a credential the user believes is gone
// would still be served by a lower-priority store.
func TestChainDeleteClearsEveryStore(t *testing.T) {
	ctx := context.Background()
	key := credential.ProviderKey("openai")

	first := credential.NewMemoryStore(map[string]string{key: "one"})
	second := credential.NewMemoryStore(map[string]string{key: "two"})
	chain := credential.NewChain(first, second)

	if err := chain.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := second.Get(ctx, key); !errors.Is(err, credential.ErrNotFound) {
		t.Fatal("the shadowed credential in the second store survived the delete")
	}
	if err := chain.Delete(ctx, key); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("second delete got %v, want ErrNotFound", err)
	}
}

func TestOpenBuildsTheRequestedChain(t *testing.T) {
	vaultPath := filepath.Join(t.TempDir(), "credentials.vault")

	t.Run("env only", func(t *testing.T) {
		chain, err := credential.Open(credential.Options{Backend: credential.BackendEnv})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if chain.Name() != "env" {
			t.Fatalf("got %q, want env", chain.Name())
		}
	})

	t.Run("vault", func(t *testing.T) {
		chain, err := credential.Open(credential.Options{
			Backend:   credential.BackendVault,
			VaultPath: vaultPath,
		})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if chain.Name() != "env+vault" {
			t.Fatalf("got %q, want env+vault", chain.Name())
		}
	})

	// The environment must come first in every chain: containers and CI have
	// no keychain, and probing one on every lookup would be pure overhead.
	t.Run("auto starts with env", func(t *testing.T) {
		chain, err := credential.Open(credential.Options{VaultPath: vaultPath})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if first := chain.Stores()[0].Name(); first != "env" {
			t.Fatalf("first backend is %q, want env", first)
		}
	})

	t.Run("unknown backend", func(t *testing.T) {
		if _, err := credential.Open(credential.Options{Backend: "hashicorp"}); err == nil {
			t.Fatal("expected an unknown backend to be rejected")
		}
	})
}

func TestProviderKeyIsNamespaced(t *testing.T) {
	if got := credential.ProviderKey("openai"); got != "provider/openai" {
		t.Fatalf("got %q, want provider/openai", got)
	}
}
