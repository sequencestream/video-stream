package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
)

// SchemaVersion is the document shape this binary writes.
//
// v2 added continuity_group and generation_batch to a seg and style_anchor to
// the render profile, and folded the style anchor into the render cache key.
const SchemaVersion = 2

// schemaVersionField is the one key every stored document must carry.
const schemaVersionField = "schema_version"

// Migration failures callers branch on.
var (
	// ErrSchemaVersionMissing means the document carries no schema_version. It
	// is an error rather than an assumed default: guessing a version is how a
	// document silently gets migrated by the wrong steps.
	ErrSchemaVersionMissing = errors.New("document has no schema_version")
	// ErrSchemaTooNew means the document was written by a newer binary.
	ErrSchemaTooNew = errors.New("document schema is newer than this binary")
)

// Step is one migration hop.
//
// It operates on the generically decoded document rather than on a Project.
// Migrating through the current struct is a well-known trap: a field removed in
// the target version simply vanishes during unmarshalling, so the step never
// sees the data it was written to relocate.
type Step struct {
	From  int
	To    int
	Apply func(doc map[string]any) error
}

// Migrator upgrades stored documents to a target version.
type Migrator struct {
	target int
	steps  []Step
}

// NewMigrator builds a migrator that brings documents up to target, from steps
// given in any order. Steps must form an unbroken chain up to target; a gap is
// reported when a document actually needs it.
//
// The target is a parameter rather than SchemaVersion so the chain logic can be
// exercised against a longer history than the one that exists today.
func NewMigrator(target int, steps ...Step) *Migrator {
	sorted := append([]Step{}, steps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].From < sorted[j].From })
	return &Migrator{target: target, steps: sorted}
}

// DefaultMigrator is the migrator applied to stored projects.
var DefaultMigrator = NewMigrator(SchemaVersion, stepV1ToV2)

// stepV1ToV2 reseals every derived field.
//
// v2 added style_anchor to the render cache key and moved its prefix to rk2:,
// so every v1 render_cache_key now disagrees with a recomputation. Left alone,
// such a document reads back fine and then fails Validate on the next save with
// ErrStaleDerived — an error blaming the caller for something the schema bump
// did.
//
// The step round-trips through the Project struct, which Step's doc comment
// warns against in general: a field the target version dropped disappears
// during unmarshalling, so the step never sees the data it was meant to move.
// That is safe here for one specific reason — v2 is a pure superset of v1 and
// removes nothing. A future step that drops a field must not copy this.
var stepV1ToV2 = Step{From: 1, To: 2, Apply: func(doc map[string]any) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("re-encode document: %w", err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("decode document as a project: %w", err)
	}
	p.Seal()

	resealed, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode resealed project: %w", err)
	}
	var next map[string]any
	if err := json.Unmarshal(resealed, &next); err != nil {
		return fmt.Errorf("decode resealed project: %w", err)
	}

	clear(doc)
	maps.Copy(doc, next)
	return nil
}}

// Migrate upgrades raw to the migrator's target version and returns the
// rewritten document. A document already at the target is returned unchanged.
func (m *Migrator) Migrate(raw []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode document: %w", err)
	}

	version, err := documentVersion(doc)
	if err != nil {
		return nil, err
	}
	if version > m.target {
		return nil, fmt.Errorf("%w: document is v%d, this binary understands v%d", ErrSchemaTooNew, version, m.target)
	}
	if version == m.target {
		return raw, nil
	}

	for version < m.target {
		step, ok := m.stepFrom(version)
		if !ok {
			return nil, fmt.Errorf("no migration from v%d to v%d", version, m.target)
		}
		// A step that lands past the target would leave the document stamped
		// with a version this binary cannot read, and Migrate would return it
		// with a nil error.
		if step.To <= step.From || step.To > m.target {
			return nil, fmt.Errorf("migration from v%d lands on v%d, outside the v%d target", step.From, step.To, m.target)
		}
		if err := step.Apply(doc); err != nil {
			return nil, fmt.Errorf("migrate v%d to v%d: %w", step.From, step.To, err)
		}
		version = step.To
		doc[schemaVersionField] = float64(version)
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode migrated document: %w", err)
	}
	return out, nil
}

func (m *Migrator) stepFrom(version int) (Step, bool) {
	for _, s := range m.steps {
		if s.From == version {
			return s, true
		}
	}
	return Step{}, false
}

func documentVersion(doc map[string]any) (int, error) {
	raw, ok := doc[schemaVersionField]
	if !ok {
		return 0, ErrSchemaVersionMissing
	}
	n, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("%s must be a number, got %T", schemaVersionField, raw)
	}
	if n < 1 {
		return 0, fmt.Errorf("%s must be at least 1, got %v", schemaVersionField, n)
	}
	return int(n), nil
}
