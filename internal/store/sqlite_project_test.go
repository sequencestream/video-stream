package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
)

func openStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, _ := openStoreAt(t)
	return s
}

func openStoreAt(t *testing.T) (*store.SQLiteStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video-stream.db")
	s, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// bumpStoredSchemaVersion forges a document written by a future binary. It goes
// behind the store on purpose: no exported call may produce this state, which
// is exactly why the read path has to defend against it.
func bumpStoredSchemaVersion(t *testing.T, path, id string, version int) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw handle: %v", err)
	}
	defer db.Close()

	var document string
	if err := db.QueryRow(`SELECT document FROM projects WHERE id = ?`, id).Scan(&document); err != nil {
		t.Fatalf("read stored document: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(document), &doc); err != nil {
		t.Fatalf("decode stored document: %v", err)
	}
	doc["schema_version"] = version

	rewritten, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode stored document: %v", err)
	}
	if _, err := db.Exec(`UPDATE projects SET document = ? WHERE id = ?`, string(rewritten), id); err != nil {
		t.Fatalf("write stored document: %v", err)
	}
}

func sampleProject(t *testing.T, id string) model.Project {
	t.Helper()

	intro := model.NewSeg("intro", "增量重编译是这件事的支点", 2000)
	intro.SubtitleBreaks = []int{5}
	intro.VisualPromptSlot = "opening"

	body := model.NewSeg("body", "预算写成定值，缓存就永远命不中", 3000)
	body.EmotionTag = model.EmotionSerious
	body.Breath = model.BreathShort
	body.DependsOn = []string{"intro"}
	body.Protected = true

	p := model.NewProject(id, "cache design", time.UnixMilli(1_700_000_000_000))
	p.Segs = []model.Seg{intro, body}
	p.Timeline = model.Timeline{Events: []model.Event{{
		ID:   "e1",
		Kind: model.EventSpeech,
		Utterances: []model.Utterance{
			{ID: "u1", SegID: "intro", Tokens: []model.Token{
				{ID: "t1", Text: "增量", StartMS: 0, EndMS: 400, Confidence: 0.98, Source: model.SourceTTSAlign, EditState: model.EditKept},
				{ID: "t2", Text: "重编译", StartMS: 400, EndMS: 950, Confidence: 0.91, Source: model.SourceTTSAlign, EditState: model.EditKept},
			}},
			{ID: "u2", SegID: "body", Tokens: []model.Token{
				{ID: "t3", Text: "预算", StartMS: 1000, EndMS: 1400, Confidence: 1, Source: model.SourceTTSAlign, EditState: model.EditKept},
			}},
		},
	}}}
	p.Seal()

	if err := p.Validate(); err != nil {
		t.Fatalf("the fixture itself is invalid: %v", err)
	}
	return p
}

func TestSaveProjectRoundTripsEveryField(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	want := sampleProject(t, "p1")

	if err := s.SaveProject(ctx, want); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	got, err := s.GetProject(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the project:\n got %+v\nwant %+v", got, want)
	}
	// A nil audio_source must come back nil rather than as a zero struct: an
	// empty AudioSource would be an unknown kind and fail validation.
	if got.Segs[0].AudioSource != nil {
		t.Fatal("a nil audio_source came back populated")
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("the reloaded project no longer validates: %v", err)
	}
}

func TestGetProjectReportsAMissingID(t *testing.T) {
	if _, err := openStore(t).GetProject(context.Background(), "nope"); !errors.Is(err, store.ErrProjectNotFound) {
		t.Fatalf("got %v, want ErrProjectNotFound", err)
	}
}

// The store is the boundary: once an inconsistent document is on disk every
// later reader has to cope with it.
func TestSaveProjectRejectsAnInvalidProjectWithoutWriting(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	p := sampleProject(t, "p1")
	p.Segs[0].Text = "edited without re-sealing"

	if err := s.SaveProject(ctx, p); !errors.Is(err, model.ErrStaleDerived) {
		t.Fatalf("got %v, want ErrStaleDerived", err)
	}
	if _, err := s.GetProject(ctx, "p1"); !errors.Is(err, store.ErrProjectNotFound) {
		t.Fatalf("a rejected project was written anyway: %v", err)
	}
}

func TestSaveProjectRejectsAFixedDurationBudget(t *testing.T) {
	s := openStore(t)

	p := sampleProject(t, "p1")
	p.Segs[0].DurationBudget = model.DurationBudget{MinMS: 2000, MaxMS: 2000}
	p.Seal()

	if err := s.SaveProject(context.Background(), p); !errors.Is(err, model.ErrFixedDurationBudget) {
		t.Fatalf("got %v, want ErrFixedDurationBudget", err)
	}
}

// The seg index is a projection, so a seg removed from the document must
// disappear from it. A leftover row would let the render cache hand back an
// artifact for a seg that no longer exists.
func TestSaveProjectRebuildsTheSegIndex(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	p := sampleProject(t, "p1")
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	droppedKey := p.Segs[1].RenderCacheKey

	p.Segs = p.Segs[:1]
	p.Timeline.Events[0].Utterances = p.Timeline.Events[0].Utterances[:1]
	p.Seal()
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatalf("SaveProject after removing a seg: %v", err)
	}

	refs, err := s.SegsByRenderCacheKey(ctx, droppedKey)
	if err != nil {
		t.Fatalf("SegsByRenderCacheKey: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("the removed seg is still indexed: %+v", refs)
	}

	summaries, err := s.ListProjects(ctx, 10)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(summaries) != 1 || summaries[0].SegCount != 1 {
		t.Fatalf("got %+v, want one project holding one seg", summaries)
	}
}

// Identical wording in two projects must share a cache key, which is the whole
// point of leaving seg_id and project id out of the hash.
func TestSegsByRenderCacheKeyMatchesAcrossProjects(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	first := sampleProject(t, "p1")
	second := sampleProject(t, "p2")
	for _, p := range []model.Project{first, second} {
		if err := s.SaveProject(ctx, p); err != nil {
			t.Fatalf("SaveProject %s: %v", p.ID, err)
		}
	}

	key := first.Segs[0].RenderCacheKey
	refs, err := s.SegsByRenderCacheKey(ctx, key)
	if err != nil {
		t.Fatalf("SegsByRenderCacheKey: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("got %d refs, want one per project: %+v", len(refs), refs)
	}
	for _, ref := range refs {
		if ref.SegID != "intro" || ref.RenderCacheKey != key {
			t.Fatalf("unexpected ref %+v", ref)
		}
		if ref.DurationBudget != first.Segs[0].DurationBudget {
			t.Fatalf("ref %+v lost its budget", ref)
		}
	}

	// The budget travels with the ref because a key match alone is not a cache
	// hit; the artifact's real duration still has to fit.
	if !first.Segs[0].CanReuse(refs[0].RenderCacheKey, refs[0].DurationBudget.TargetMS()) {
		t.Fatal("a key match with an in-budget duration should be reusable")
	}
	if first.Segs[0].CanReuse(refs[0].RenderCacheKey, refs[0].DurationBudget.MaxMS+1) {
		t.Fatal("a key match with an over-budget duration must not be reusable")
	}
}

func TestSegsByRenderCacheKeyCarriesTheProtectedFlag(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	p := sampleProject(t, "p1")
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	refs, err := s.SegsByRenderCacheKey(ctx, p.Segs[1].RenderCacheKey)
	if err != nil {
		t.Fatalf("SegsByRenderCacheKey: %v", err)
	}
	if len(refs) != 1 || !refs[0].Protected {
		t.Fatalf("got %+v, want the protected seg", refs)
	}
}

func TestDeleteProjectClearsTheSegIndex(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	p := sampleProject(t, "p1")
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	if err := s.DeleteProject(ctx, "p1"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	refs, err := s.SegsByRenderCacheKey(ctx, p.Segs[0].RenderCacheKey)
	if err != nil {
		t.Fatalf("SegsByRenderCacheKey: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("the seg index survived the delete: %+v", refs)
	}
	if err := s.DeleteProject(ctx, "p1"); !errors.Is(err, store.ErrProjectNotFound) {
		t.Fatalf("got %v, want ErrProjectNotFound on the second delete", err)
	}
}

func TestListProjectsIsNewestFirst(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	older := sampleProject(t, "older")
	newer := sampleProject(t, "newer")
	newer.UpdatedAt = older.UpdatedAt.Add(time.Hour)

	for _, p := range []model.Project{older, newer} {
		if err := s.SaveProject(ctx, p); err != nil {
			t.Fatalf("SaveProject %s: %v", p.ID, err)
		}
	}

	got, err := s.ListProjects(ctx, 10)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(got) != 2 || got[0].ID != "newer" {
		t.Fatalf("got %+v, want the newer project first", got)
	}
	if got[0].SegCount != 2 || got[0].Title != "cache design" {
		t.Fatalf("summary %+v lost its metadata", got[0])
	}
}

// Stored documents pass through the migrator on read, so a document written by
// a future binary must be refused rather than silently decoded with fields
// dropped.
func TestGetProjectRefusesADocumentFromANewerBinary(t *testing.T) {
	s, path := openStoreAt(t)
	ctx := context.Background()

	p := sampleProject(t, "p1")
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	bumpStoredSchemaVersion(t, path, "p1", model.SchemaVersion+1)

	if _, err := s.GetProject(ctx, "p1"); !errors.Is(err, model.ErrSchemaTooNew) {
		t.Fatalf("got %v, want ErrSchemaTooNew", err)
	}
}
