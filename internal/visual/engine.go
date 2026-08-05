package visual

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/recompile"
	"github.com/sequencestream/video-stream/internal/store"
	"github.com/sequencestream/video-stream/internal/telemetry"
)

// Store persists style packs.
type Store interface {
	PutStylePack(ctx context.Context, p store.StylePackRecord) error
	StylePack(ctx context.Context, id string) (store.StylePackRecord, error)
	StylePacks(ctx context.Context) ([]store.StylePackRecord, error)
}

// Options configures the Engine.
type Options struct {
	Store    Store
	Reporter telemetry.Reporter
	Logger   *slog.Logger
}

// Engine manages L2 style packs.
type Engine struct {
	store    Store
	reporter telemetry.Reporter
	logger   *slog.Logger
}

var (
	ErrNoStore      = errors.New("visual has no store configured")
	ErrPackNotFound = errors.New("style pack not found")
)

// New builds an Engine.
func New(opts Options) *Engine {
	e := &Engine{store: opts.Store, reporter: opts.Reporter, logger: opts.Logger}
	if e.reporter == nil {
		e.reporter = telemetry.Nop()
	}
	if e.logger == nil {
		e.logger = slog.New(slog.DiscardHandler)
	}
	return e
}

// Create stores a new style pack.
func (e *Engine) Create(ctx context.Context, p StylePack) (StylePack, error) {
	if e.store == nil {
		return StylePack{}, ErrNoStore
	}
	if p.ID == "" {
		p.ID = newPackID()
	}
	p.Seal()
	if err := p.Validate(); err != nil {
		return StylePack{}, err
	}
	doc, err := json.Marshal(p)
	if err != nil {
		return StylePack{}, err
	}
	if err := e.store.PutStylePack(ctx, store.StylePackRecord{
		ID: p.ID, Name: p.Name, SchemaVersion: p.SchemaVersion, Document: string(doc),
	}); err != nil {
		return StylePack{}, err
	}
	return p, nil
}

// Get returns one pack.
func (e *Engine) Get(ctx context.Context, id string) (StylePack, error) {
	if e.store == nil {
		return StylePack{}, ErrNoStore
	}
	r, err := e.store.StylePack(ctx, id)
	if errors.Is(err, store.ErrStylePackNotFound) {
		return StylePack{}, ErrPackNotFound
	}
	if err != nil {
		return StylePack{}, err
	}
	return decodeRecord(r)
}

// List returns all packs.
func (e *Engine) List(ctx context.Context) ([]StylePack, error) {
	if e.store == nil {
		return nil, ErrNoStore
	}
	records, err := e.store.StylePacks(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StylePack, 0, len(records))
	for _, r := range records {
		p, err := decodeRecord(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Import parses and stores an exported pack (new id if collision).
func (e *Engine) Import(ctx context.Context, data []byte) (StylePack, error) {
	p, err := ImportJSON(data)
	if err != nil {
		return StylePack{}, err
	}
	p.ID = ""
	return e.Create(ctx, p)
}

// Export serialises a pack.
func (e *Engine) Export(ctx context.Context, id string) ([]byte, error) {
	p, err := e.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return p.ExportJSON()
}

// ApplyResult is returned when attaching a pack to a project.
type ApplyResult struct {
	Project           model.Project `json:"project"`
	StyleSeed         string        `json:"style_seed"`
	FullRerunWarning  string        `json:"full_rerun_warning"`
	RequiresFullRerun bool          `json:"requires_full_rerun"`
}

// Apply sets the project's style anchor to the pack.
func (e *Engine) Apply(_ context.Context, pack StylePack, project model.Project) ApplyResult {
	prev := project.RenderProfile.StyleAnchor
	project.RenderProfile.StyleAnchor = pack.StyleAnchorID()
	project.Seal()

	changed := prev != project.RenderProfile.StyleAnchor
	warning := ""
	if changed {
		warning = FullRerunWarning
	}
	return ApplyResult{
		Project: project, StyleSeed: pack.StyleSeed,
		FullRerunWarning: warning, RequiresFullRerun: changed,
	}
}

// PlanPackSwitch delegates to recompile to verify full rerun on anchor change.
func PlanPackSwitch(previous, next model.Project, cache recompile.Cache) (recompile.Plan, error) {
	return recompile.New(recompile.Options{Cache: cache}).Plan(context.Background(), previous, next)
}

func decodeRecord(r store.StylePackRecord) (StylePack, error) {
	var p StylePack
	if err := json.Unmarshal([]byte(r.Document), &p); err != nil {
		return StylePack{}, fmt.Errorf("decode style pack %s: %w", r.ID, err)
	}
	p.Seal()
	return p, nil
}

func newPackID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
