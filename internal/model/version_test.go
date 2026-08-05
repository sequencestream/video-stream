package model_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
)

// v1 is the first schema version, so DefaultMigrator holds no steps and there
// is no real upgrade to exercise. The chain logic is instead driven through a
// migrator built for a longer, fictional history; registering a fake historical
// migration in production code just to have something to test would be worse.

func TestDefaultMigratorLeavesACurrentDocumentUntouched(t *testing.T) {
	raw := []byte(fmt.Sprintf(`{"schema_version":%d,"id":"p1"}`, model.SchemaVersion))

	got, err := model.DefaultMigrator.Migrate(raw)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("got %s, want the input back unchanged", got)
	}
}

// Steps must run in order and each must see what the previous one wrote,
// otherwise a two-hop upgrade would silently drop the intermediate rewrite.
func TestMigratorAppliesStepsInOrder(t *testing.T) {
	var trace []string
	m := model.NewMigrator(3,
		model.Step{From: 2, To: 3, Apply: func(doc map[string]any) error {
			trace = append(trace, "2->3")
			title, _ := doc["title"].(string)
			if !strings.HasPrefix(title, "v2:") {
				return fmt.Errorf("step 2->3 ran before 1->2, saw title %q", title)
			}
			doc["title"] = title + " v3"
			return nil
		}},
		model.Step{From: 1, To: 2, Apply: func(doc map[string]any) error {
			trace = append(trace, "1->2")
			title, _ := doc["title"].(string)
			doc["title"] = "v2:" + title
			return nil
		}},
	)

	out, err := m.Migrate([]byte(`{"schema_version":1,"title":"draft"}`))
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode migrated document: %v", err)
	}
	if got := doc["title"]; got != "v2:draft v3" {
		t.Fatalf("title = %v, want %q", got, "v2:draft v3")
	}
	if got := doc["schema_version"]; got != float64(3) {
		t.Fatalf("schema_version = %v, want 3", got)
	}
	if len(trace) != 2 || trace[0] != "1->2" || trace[1] != "2->3" {
		t.Fatalf("steps ran as %v, want [1->2 2->3]", trace)
	}
}

// A field a later version no longer models still has to be visible to the step
// that relocates it. That only holds because migrations run on the generically
// decoded document rather than on a Project.
func TestMigratorSeesFieldsTheCurrentStructDoesNotModel(t *testing.T) {
	m := model.NewMigrator(2, model.Step{From: 1, To: 2, Apply: func(doc map[string]any) error {
		legacy, ok := doc["speaker_name"]
		if !ok {
			return errors.New("the v1-only field was not visible to the step")
		}
		delete(doc, "speaker_name")
		doc["render_profile"] = map[string]any{"voice": legacy}
		return nil
	}})

	out, err := m.Migrate([]byte(`{"schema_version":1,"id":"p1","speaker_name":"xiaoyun"}`))
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("decode migrated document: %v", err)
	}
	profile, ok := doc["render_profile"].(map[string]any)
	if !ok || profile["voice"] != "xiaoyun" {
		t.Fatalf("the legacy field was not relocated, got %v", doc)
	}
	if _, still := doc["speaker_name"]; still {
		t.Fatal("the legacy field should have been removed")
	}
}

func TestMigratorReportsAGapInTheChain(t *testing.T) {
	m := model.NewMigrator(3, model.Step{From: 1, To: 2, Apply: func(map[string]any) error { return nil }})

	_, err := m.Migrate([]byte(`{"schema_version":1}`))
	if err == nil || !strings.Contains(err.Error(), "no migration from v2") {
		t.Fatalf("got %v, want a missing-step error", err)
	}
}

// A step that jumps past the target would stamp the document with a version
// this binary cannot read and hand it back with a nil error — the exact
// silent-corruption case the version field exists to catch.
func TestMigratorRefusesAStepThatOvershootsTheTarget(t *testing.T) {
	runs := []struct {
		name string
		step model.Step
	}{
		{"lands past the target", model.Step{From: 1, To: 3}},
		{"does not advance", model.Step{From: 1, To: 1}},
		{"goes backwards", model.Step{From: 1, To: 0}},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			run.step.Apply = func(map[string]any) error { return nil }
			m := model.NewMigrator(2, run.step)

			out, err := m.Migrate([]byte(`{"schema_version":1}`))
			if err == nil {
				t.Fatalf("migration was accepted, returning %s", out)
			}
			if !strings.Contains(err.Error(), "outside the v2 target") {
				t.Fatalf("got %v, want an out-of-range step error", err)
			}
		})
	}
}

func TestMigratorPropagatesAStepFailure(t *testing.T) {
	boom := errors.New("boom")
	m := model.NewMigrator(2, model.Step{From: 1, To: 2, Apply: func(map[string]any) error { return boom }})

	_, err := m.Migrate([]byte(`{"schema_version":1}`))
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the step's own error", err)
	}
}

// Downgrading would mean an old binary rewrites a document it does not fully
// understand, silently dropping whatever fields it has no field for.
func TestMigrateRefusesADocumentFromANewerBinary(t *testing.T) {
	raw := []byte(fmt.Sprintf(`{"schema_version":%d}`, model.SchemaVersion+1))

	_, err := model.DefaultMigrator.Migrate(raw)
	if !errors.Is(err, model.ErrSchemaTooNew) {
		t.Fatalf("got %v, want ErrSchemaTooNew", err)
	}
}

func TestMigrateRefusesADocumentWithoutAVersion(t *testing.T) {
	_, err := model.DefaultMigrator.Migrate([]byte(`{"id":"p1"}`))
	if !errors.Is(err, model.ErrSchemaVersionMissing) {
		t.Fatalf("got %v, want ErrSchemaVersionMissing", err)
	}
}

func TestMigrateRefusesAMalformedVersion(t *testing.T) {
	runs := []struct {
		name string
		raw  string
	}{
		{"not a number", `{"schema_version":"1"}`},
		{"below one", `{"schema_version":0}`},
		{"not an object", `["schema_version"]`},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			if _, err := model.DefaultMigrator.Migrate([]byte(run.raw)); err == nil {
				t.Fatalf("%s should have been rejected", run.name)
			}
		})
	}
}
