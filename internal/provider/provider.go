// Package provider turns a configured model provider plus a stored credential
// into a model call.
//
// It is the only place in the process that holds a decrypted API key, and it
// holds one for the duration of a single request: the key is fetched from the
// credential store inside Complete, handed to the client, and never written to
// a field. A Registry keeps a reference to the store, not to any secret.
//
// Only the OpenAI Chat Completions protocol is implemented. Most vendors expose
// a compatible endpoint, so pointing base_url at them is enough; Anthropic's
// native Messages API has a different wire shape and will need a second branch
// here when a later intent needs it.
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vogo/aimodel/provider/openai"

	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/credential"
	"github.com/sequencestream/video-stream/internal/redact"
)

// ErrNoCredential means the provider is configured but has no stored key.
// It is distinct from a transport failure so the task receipt can tell the user
// to run `vs credential set` rather than to check their network.
var ErrNoCredential = errors.New("no credential for provider")

// ErrUnknownProvider means the name is not in the configuration.
var ErrUnknownProvider = errors.New("unknown provider")

// defaultTimeout bounds a completion call. Model APIs can hang for minutes, and
// a task worker blocked forever is indistinguishable from a deadlock.
const defaultTimeout = 90 * time.Second

// Registry builds model clients on demand.
type Registry struct {
	providers []config.Provider
	creds     credential.Store
	timeout   time.Duration
}

// Options configures a Registry.
type Options struct {
	Providers   []config.Provider
	Credentials credential.Store
	// Timeout bounds one completion call. Zero means defaultTimeout.
	Timeout time.Duration
}

// New builds a registry.
func New(opts Options) *Registry {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	return &Registry{
		providers: opts.Providers,
		creds:     opts.Credentials,
		timeout:   opts.Timeout,
	}
}

// Names lists the configured provider names in declaration order, so the first
// one can serve as the default.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.providers))
	for _, p := range r.providers {
		names = append(names, p.Name)
	}
	return names
}

// Completion is the protocol-neutral result of one model call.
type Completion struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Text             string `json:"text"`
	FinishReason     string `json:"finish_reason,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
}

// Request is one completion request.
type Request struct {
	// Provider names the configured provider. Empty uses the first one.
	Provider string
	// Model overrides the provider's configured model.
	Model string
	// System is an optional system instruction.
	System string
	// Prompt is the user message.
	Prompt string
	// MaxTokens caps the response. Zero leaves it to the backend.
	MaxTokens int
}

// Complete runs one completion.
func (r *Registry) Complete(ctx context.Context, req Request) (Completion, error) {
	provider, err := r.resolve(req.Provider)
	if err != nil {
		return Completion{}, err
	}

	// The key is a local variable for the lifetime of this call and is never
	// stored on the Registry or the client we hand back to the caller.
	apiKey, err := r.credential(ctx, provider.Name)
	if err != nil {
		return Completion{}, err
	}

	// Registering the key means that if it ever ends up inside an error
	// message or a log line from here on, it is replaced before being written.
	redact.Register(apiKey)

	model := req.Model
	if model == "" {
		model = provider.Model
	}
	if model == "" {
		return Completion{}, fmt.Errorf("provider %q has no model configured and none was requested", provider.Name)
	}

	options := []openai.ClientOption{openai.WithTimeout(r.timeout)}
	if provider.BaseURL != "" {
		options = append(options, openai.WithBaseURL(provider.BaseURL))
	}
	client := openai.NewClient(apiKey, options...)

	messages := make([]openai.ChatCompletionMessage, 0, 2)
	if req.System != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.RoleSystem,
			Content: openai.NewTextContent(req.System),
		})
	}
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.RoleUser,
		Content: openai.NewTextContent(req.Prompt),
	})

	request := &openai.ChatCompletionRequest{Model: model, Messages: messages}
	if req.MaxTokens > 0 {
		// MaxCompletionTokens rather than the deprecated MaxTokens: it is the
		// only cap reasoning models accept.
		request.MaxCompletionTokens = &req.MaxTokens
	}

	response, err := client.ChatCompletions(ctx, request)
	if err != nil {
		// A failing request can echo the URL or headers, so the error is
		// redacted before it reaches a log or a task receipt.
		return Completion{}, redact.Error(fmt.Errorf("provider %q: %w", provider.Name, err))
	}
	if len(response.Choices) == 0 {
		return Completion{}, fmt.Errorf("provider %q returned no choices", provider.Name)
	}

	choice := response.Choices[0]
	completion := Completion{
		Provider: provider.Name,
		Model:    response.Model,
		Text:     choice.Message.Content.Text(),
	}
	if choice.FinishReason != nil {
		completion.FinishReason = *choice.FinishReason
	}
	if usage := response.Usage; usage != nil {
		completion.PromptTokens = usage.PromptTokens
		completion.CompletionTokens = usage.CompletionTokens
		completion.TotalTokens = usage.TotalTokens
	}
	return completion, nil
}

// resolve finds the requested provider, defaulting to the first configured one.
func (r *Registry) resolve(name string) (config.Provider, error) {
	if len(r.providers) == 0 {
		return config.Provider{}, errors.New("no model providers are configured")
	}
	if name == "" {
		return r.providers[0], nil
	}

	for _, p := range r.providers {
		if p.Name == name {
			return p, nil
		}
	}
	return config.Provider{}, fmt.Errorf("%w: %q; configured: %v", ErrUnknownProvider, name, r.Names())
}

// credential fetches the provider's key, translating a miss into actionable
// guidance rather than a bare "not found".
func (r *Registry) credential(ctx context.Context, name string) (string, error) {
	if r.creds == nil {
		return "", fmt.Errorf("%w: %q (no credential store configured)", ErrNoCredential, name)
	}

	apiKey, err := r.creds.Get(ctx, credential.ProviderKey(name))
	if errors.Is(err, credential.ErrNotFound) {
		return "", fmt.Errorf("%w: %q; store one with `vs credential set %s`", ErrNoCredential, name, name)
	}
	if err != nil {
		return "", fmt.Errorf("read credential for provider %q: %w", name, err)
	}
	return apiKey, nil
}
