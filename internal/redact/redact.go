// Package redact removes secrets from anything the process emits: structured
// logs, task receipts and error strings.
//
// Redaction is the second line of defence, not the first. The first is not
// putting secrets into long-lived structures that get serialised at all, which
// is why config.Provider carries no API key field and provider clients hold a
// credential.Store rather than a resolved secret. Treat this package as the net
// that catches the cases the first line missed, never as permission to pass
// secrets around freely.
//
// Two independent layers run, because either alone leaves a real gap:
//
//   - By key: a log attribute named "api_key", "token", "password" and friends
//     has its value replaced regardless of content. This catches secrets that
//     never passed through the credential store, such as a header read straight
//     off an inbound request.
//
//   - By value: secrets handed to Register are matched as substrings anywhere in
//     a string. This catches the most common leak of all — a secret interpolated
//     into an error message, e.g. fmt.Errorf("GET %s failed", urlWithKey).
package redact

import (
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// Placeholder replaces every redacted value.
const Placeholder = "[REDACTED]"

// minSecretLength is the shortest value worth registering for substring
// matching. Registering something like "abc" would smear the placeholder across
// unrelated output and make logs useless, which in practice gets redaction
// switched off entirely — a short secret is better left to the by-key layer.
const minSecretLength = 8

// sensitiveKeyParts are matched as substrings against normalised attribute
// names, so "openai_api_key", "X-Api-Key" and "apiKey" all hit the "api_key"
// rule without needing an exhaustive list of spellings.
var sensitiveKeyParts = []string{
	"api_key",
	"authorization",
	"cookie",
	"credential",
	"passphrase",
	"password",
	"private_key",
	"secret",
	"session_token",
	"token",
}

// Registry holds the secret values seen by this process so they can be matched
// as substrings. It deliberately exposes no way to read the values back: the
// only thing callers may do is add a secret and redact a string.
type Registry struct {
	mu sync.RWMutex
	// secrets is a set; the slice below keeps a length-sorted view so that
	// replacement always starts with the longest value. Without that ordering
	// a secret that is a prefix of another would be replaced first and leave
	// the remainder of the longer one exposed.
	secrets map[string]struct{}
	ordered []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{secrets: make(map[string]struct{})}
}

// defaultRegistry backs the package-level functions. Redaction has to work from
// any package that emits output without threading a registry through every call
// site, and there is exactly one process worth of secrets, so a package-level
// instance is the honest model here.
var defaultRegistry = NewRegistry()

// Default returns the process-wide registry.
func Default() *Registry { return defaultRegistry }

// Register records a secret so later output containing it is redacted. Values
// shorter than minSecretLength and blank values are ignored. Registering the
// same value twice is a no-op.
func (r *Registry) Register(secret string) {
	if len(secret) < minSecretLength || strings.TrimSpace(secret) == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.secrets[secret]; exists {
		return
	}
	r.secrets[secret] = struct{}{}

	r.ordered = append(r.ordered, secret)
	sort.SliceStable(r.ordered, func(i, j int) bool {
		return len(r.ordered[i]) > len(r.ordered[j])
	})
}

// String replaces every registered secret found in s.
func (r *Registry) String(s string) string {
	if s == "" {
		return s
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, secret := range r.ordered {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Placeholder)
		}
	}
	return s
}

// Register records a secret in the process-wide registry.
func Register(secret string) { defaultRegistry.Register(secret) }

// String redacts registered secrets from s using the process-wide registry.
func String(s string) string { return defaultRegistry.String(s) }

// Error wraps err so its message is redacted when read. A nil error stays nil,
// and the original error remains reachable through errors.Is/errors.As so
// wrapping never breaks sentinel matching.
func Error(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{err: err}
}

type redactedError struct{ err error }

func (e redactedError) Error() string { return String(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }

// IsSensitiveKey reports whether an attribute with this name should have its
// value replaced outright.
func IsSensitiveKey(key string) bool {
	normalised := normaliseKey(key)
	for _, part := range sensitiveKeyParts {
		if strings.Contains(normalised, part) {
			return true
		}
	}
	return false
}

// normaliseKey lowercases and collapses the separators that distinguish the
// same field name across conventions: HTTP headers use "X-Api-Key", Go structs
// use "APIKey", JSON uses "api_key". Stripping separators lets one rule cover
// all three.
func normaliseKey(key string) string {
	var b strings.Builder
	b.Grow(len(key) + 1)
	for _, r := range strings.ToLower(key) {
		switch r {
		case '-', '.', ' ':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}

	normalised := b.String()
	// "apikey" written without any separator still has to match "api_key".
	return strings.ReplaceAll(normalised, "apikey", "api_key")
}

// Attr redacts one slog attribute, recursing into groups. It is shaped for use
// as slog.HandlerOptions.ReplaceAttr, which is how logging.New installs it.
func Attr(_ []string, attr slog.Attr) slog.Attr {
	if IsSensitiveKey(attr.Key) {
		return slog.String(attr.Key, Placeholder)
	}

	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.String(attr.Key, String(value.String()))
	case slog.KindGroup:
		attrs := value.Group()
		redacted := make([]any, 0, len(attrs))
		for _, inner := range attrs {
			redacted = append(redacted, Attr(nil, inner))
		}
		return slog.Group(attr.Key, redacted...)
	case slog.KindAny:
		// Errors are the single most common carrier of an interpolated
		// secret, so they are converted to a redacted string rather than
		// left for the handler to format verbatim.
		if err, ok := value.Any().(error); ok {
			return slog.String(attr.Key, String(err.Error()))
		}
		return attr
	default:
		return attr
	}
}

// Map returns a copy of attrs with sensitive keys and registered secrets
// redacted. Task receipts go through this before being persisted.
func Map(attrs map[string]any) map[string]any {
	if attrs == nil {
		return nil
	}

	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if IsSensitiveKey(k) {
			out[k] = Placeholder
			continue
		}
		out[k] = value(v)
	}
	return out
}

// value redacts a single value, recursing through the shapes a task payload or
// result can hold after JSON decoding.
func value(v any) any {
	switch typed := v.(type) {
	case string:
		return String(typed)
	case error:
		return String(typed.Error())
	case map[string]any:
		return Map(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = value(item)
		}
		return out
	default:
		return v
	}
}
