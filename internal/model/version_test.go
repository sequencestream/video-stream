package model_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
)

// DefaultMigrator holds exactly one real step (v1 to v2), which the tests at
// the bottom of this file exercise directly. The chain logic — ordering, gaps,
// overshoot — needs a longer history than the one that exists, so it is driven
// through migrators built for a fictional one; registering fake historical
// migrations in production code just to have something to test would be worse.

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

// v1Document forges a document as v1 would have written it: sealed by this
// binary, then stamped back to v1 with the pre-rk2 cache keys a v1 seal would
// have produced. No exported call can produce this state, which is precisely
// why the migration exists.
func v1Document(t *testing.T, p model.Project) []byte {
	t.Helper()

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("encode project: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	doc["schema_version"] = float64(1)

	segs, _ := doc["segs"].([]any)
	for i, entry := range segs {
		seg, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("seg %d is not an object", i)
		}
		key, _ := seg["render_cache_key"].(string)
		// A v1 key had the old prefix and no style anchor folded in; the digest
		// after the prefix is irrelevant, only that it disagrees with a v2
		// recomputation.
		seg["render_cache_key"] = "rk1:" + strings.TrimPrefix(key, "rk2:")
	}

	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode v1 document: %v", err)
	}
	return out
}

// Every v1 render_cache_key disagrees with a v2 recomputation, so a document
// that skipped the migration would read back fine and then fail Validate on the
// next save with ErrStaleDerived — blaming the caller for the schema bump.
func TestMigratingV1ResealsTheRenderCacheKeys(t *testing.T) {
	a := model.NewSeg("a", "first line", 1000)
	b := model.NewSeg("b", "second line", 1000)
	b.DependsOn = []string{"a"}
	want := newTestProject(t, a, b)

	out, err := model.DefaultMigrator.Migrate(v1Document(t, want))
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var got model.Project
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode migrated project: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the migrated project is still stale: %v", err)
	}
	for i, seg := range got.Segs {
		if seg.RenderCacheKey != want.Segs[i].RenderCacheKey {
			t.Fatalf("seg %s key = %q, want %q", seg.SegID, seg.RenderCacheKey, want.Segs[i].RenderCacheKey)
		}
	}
}

// Resealing must not quietly rewrite authored content while it fixes the
// derived fields; only the keys were wrong.
func TestMigratingV1PreservesAuthoredContent(t *testing.T) {
	seg := model.NewSeg("a", "增量重编译是这件事的支点", 2000)
	seg.EmotionTag = model.EmotionSerious
	seg.Protected = true
	seg.SubtitleBreaks = []int{3}
	want := newTestProject(t, seg)

	out, err := model.DefaultMigrator.Migrate(v1Document(t, want))
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var got model.Project
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode migrated project: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migration changed the project:\n got %+v\nwant %+v", got, want)
	}
}

// The v2 fields are all optional, so a v1 document that never had them must
// come out with their zero values rather than failing to decode.
func TestMigratingV1LeavesTheNewFieldsEmpty(t *testing.T) {
	p := newTestProject(t, model.NewSeg("a", "first line", 1000))

	out, err := model.DefaultMigrator.Migrate(v1Document(t, p))
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var got model.Project
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode migrated project: %v", err)
	}
	if got.Segs[0].ContinuityGroup != "" || got.Segs[0].GenerationBatch != "" {
		t.Fatalf("the v2 grouping fields were invented: %+v", got.Segs[0])
	}
	if got.RenderProfile.StyleAnchor != "" {
		t.Fatalf("the v2 style anchor was invented: %q", got.RenderProfile.StyleAnchor)
	}
	if got.SchemaVersion != 2 {
		t.Fatalf("schema_version = %d, want 2", got.SchemaVersion)
	}
}
