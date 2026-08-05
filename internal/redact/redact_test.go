package redact_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/sequencestream/video-stream/internal/logging"
	"github.com/sequencestream/video-stream/internal/redact"
)

// testSecret deliberately avoids the "sk-" shape that check-secrets.sh looks
// for, so the test suite never trips the repository's own secret scanner.
const testSecret = "unit-test-credential-value-9f3a"

func TestRegistryReplacesSecretAnywhereInString(t *testing.T) {
	r := redact.NewRegistry()
	r.Register(testSecret)

	input := "GET https://api.example.com/v1?key=" + testSecret + " failed"
	got := r.String(input)

	if strings.Contains(got, testSecret) {
		t.Fatalf("secret survived redaction: %q", got)
	}
	if !strings.Contains(got, redact.Placeholder) {
		t.Fatalf("expected placeholder in %q", got)
	}
}

// A short value must not be registered: substring-matching something like "abc"
// would smear the placeholder across unrelated output.
func TestRegistryIgnoresShortValues(t *testing.T) {
	r := redact.NewRegistry()
	r.Register("abc")

	if got := r.String("abc def"); got != "abc def" {
		t.Fatalf("short value should not be redacted, got %q", got)
	}
}

// When one secret is a prefix of another, replacing the shorter one first would
// leave the tail of the longer one in the output.
func TestRegistryRedactsLongestSecretFirst(t *testing.T) {
	short := "shared-prefix-secret"
	long := short + "-with-longer-tail"

	r := redact.NewRegistry()
	r.Register(short)
	r.Register(long)

	got := r.String("token=" + long)
	if strings.Contains(got, "-with-longer-tail") {
		t.Fatalf("tail of the longer secret leaked: %q", got)
	}
}

func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	r := redact.NewRegistry()

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.Register(fmt.Sprintf("concurrent-secret-%02d", i))
		}()
		go func() {
			defer wg.Done()
			_ = r.String("some log line")
		}()
	}
	wg.Wait()
}

func TestIsSensitiveKey(t *testing.T) {
	sensitive := []string{"api_key", "APIKey", "X-Api-Key", "authorization", "password", "vault_passphrase", "session_token", "cookie"}
	for _, key := range sensitive {
		if !redact.IsSensitiveKey(key) {
			t.Errorf("expected %q to be sensitive", key)
		}
	}

	safe := []string{"task_id", "provider", "duration", "status", "model"}
	for _, key := range safe {
		if redact.IsSensitiveKey(key) {
			t.Errorf("expected %q not to be sensitive", key)
		}
	}
}

func TestErrorWrapperRedactsMessageButKeepsSentinel(t *testing.T) {
	redact.Register(testSecret)

	sentinel := errors.New("upstream rejected the call")
	wrapped := redact.Error(fmt.Errorf("auth with %s: %w", testSecret, sentinel))

	if strings.Contains(wrapped.Error(), testSecret) {
		t.Fatalf("secret survived error redaction: %q", wrapped.Error())
	}
	// Redaction must not break error matching, otherwise callers would stop
	// wrapping errors to keep errors.Is working.
	if !errors.Is(wrapped, sentinel) {
		t.Fatal("errors.Is must still match through the redacting wrapper")
	}
}

func TestErrorWrapperPassesNilThrough(t *testing.T) {
	if err := redact.Error(nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestMapRedactsNestedValuesAndSensitiveKeys(t *testing.T) {
	redact.Register(testSecret)

	got := redact.Map(map[string]any{
		"api_key": "whatever-was-here",
		"error":   "call failed with " + testSecret,
		"nested":  map[string]any{"token": "inner", "note": testSecret},
		"list":    []any{testSecret, 42},
	})

	if got["api_key"] != redact.Placeholder {
		t.Errorf("sensitive key not redacted: %v", got["api_key"])
	}
	if s, _ := got["error"].(string); strings.Contains(s, testSecret) {
		t.Errorf("secret survived in error field: %q", s)
	}

	nested, ok := got["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested map lost its type: %T", got["nested"])
	}
	if nested["token"] != redact.Placeholder {
		t.Errorf("nested sensitive key not redacted: %v", nested["token"])
	}
	if s, _ := nested["note"].(string); strings.Contains(s, testSecret) {
		t.Errorf("secret survived in nested value: %q", s)
	}

	list, ok := got["list"].([]any)
	if !ok {
		t.Fatalf("list lost its type: %T", got["list"])
	}
	if s, _ := list[0].(string); strings.Contains(s, testSecret) {
		t.Errorf("secret survived in list: %q", s)
	}
	if list[1] != 42 {
		t.Errorf("non-string list values must pass through unchanged, got %v", list[1])
	}
}

// The by-key and by-value layers both have to hold through the real logger,
// since that is the only path log output actually takes.
func TestLoggerRedactsBothLayers(t *testing.T) {
	redact.Register(testSecret)

	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.New(&buf, logging.Options{Level: "debug", Format: format})

			logger.Info("provider call",
				"api_key", "value-that-was-never-registered",
				"error", fmt.Errorf("request with %s failed", testSecret),
				"detail", "url?key="+testSecret,
				"provider", "openai")

			out := buf.String()
			if strings.Contains(out, testSecret) {
				t.Errorf("registered secret leaked into %s output: %s", format, out)
			}
			if strings.Contains(out, "value-that-was-never-registered") {
				t.Errorf("sensitive key value leaked into %s output: %s", format, out)
			}
			if !strings.Contains(out, "openai") {
				t.Errorf("non-sensitive attribute was dropped from %s output: %s", format, out)
			}
		})
	}
}

func TestLoggerRedactsInsideGroups(t *testing.T) {
	redact.Register(testSecret)

	var buf bytes.Buffer
	logger := logging.New(&buf, logging.Options{Level: "debug", Format: "json"})
	logger.WithGroup("upstream").Info("call", "token", testSecret, "host", "api.example.com")

	if out := buf.String(); strings.Contains(out, testSecret) {
		t.Errorf("secret leaked from inside a group: %s", out)
	}
}
