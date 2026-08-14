package intake_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/intake"
	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/store"
)

// fixedProber reports a duration per line, in order, so budgets are predictable.
type fixedProber struct {
	durations []int64
	calls     []string
	err       error
}

func (p *fixedProber) ProbeMS(_ context.Context, text, _ string) (int64, error) {
	if p.err != nil {
		return 0, p.err
	}
	p.calls = append(p.calls, text)
	if len(p.calls) <= len(p.durations) {
		return p.durations[len(p.calls)-1], nil
	}
	return 4000, nil
}

func newStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.OpenSQLite(filepath.Join(t.TempDir(), "vs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestImportBuildsASealedProjectFromMeasuredDurations(t *testing.T) {
	s := newStore(t)
	prober := &fixedProber{durations: []int64{3840, 8832}}
	engine := intake.New(intake.Options{Projects: s, Prober: prober, Voice: "zh-CN-XiaoxiaoNeural"})

	result, err := engine.Import(context.Background(), intake.Request{
		Title:  "好好吃饭",
		Script: "今天带着小孩去看了一场电影。可能之前各种视频号已经把很多东西剧透了，对他的预期太高了。",
	})
	if err != nil {
		t.Fatal(err)
	}

	if result.SegCount != 2 || len(result.Project.Segs) != 2 {
		t.Fatalf("got %d segs, want 2", result.SegCount)
	}
	if result.TotalMS != 3840+8832 {
		t.Errorf("total_ms=%d, want %d", result.TotalMS, 3840+8832)
	}
	// The budget must be centred on the measurement: the mux stage cuts video
	// to the midpoint while the audio track runs for the measured duration.
	for i, want := range []int64{3840, 8832} {
		if got := result.Project.Segs[i].DurationBudget.TargetMS(); got != want {
			t.Errorf("seg %d budget target=%dms, want %dms", i, got, want)
		}
	}
	if err := result.Project.Validate(); err != nil {
		t.Errorf("imported project does not validate: %v", err)
	}

	stored, err := s.GetProject(context.Background(), result.Project.ID)
	if err != nil {
		t.Fatalf("imported project was not persisted: %v", err)
	}
	if stored.Title != "好好吃饭" || stored.RenderProfile.Voice != "zh-CN-XiaoxiaoNeural" {
		t.Errorf("stored project lost its title or voice: %+v", stored.RenderProfile)
	}
}

func TestImportLeavesSegsIndependent(t *testing.T) {
	engine := intake.New(intake.Options{Prober: &fixedProber{}})
	result, err := engine.Import(context.Background(), intake.Request{
		Title:  "t",
		Script: "第一句话说的是这个。第二句话说的是那个。第三句话把它们连起来。",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A linear depends_on chain would make an edit to the first line invalidate
	// every seg after it, which defeats incremental recompilation.
	for _, seg := range result.Project.Segs {
		if len(seg.DependsOn) != 0 {
			t.Errorf("seg %s depends on %v, want no dependencies", seg.SegID, seg.DependsOn)
		}
	}
}

func TestImportDerivesADistinctIDPerRun(t *testing.T) {
	stamps := []time.Time{
		time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 14, 10, 0, 1, 0, time.UTC),
	}
	var i int
	engine := intake.New(intake.Options{Prober: &fixedProber{}, Now: func() time.Time {
		i++
		return stamps[i-1]
	}})

	req := intake.Request{Title: "好好吃饭 Eat Well", Script: "第一句话说的是这个。"}
	first, err := engine.Import(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.Import(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Project.ID == second.Project.ID {
		t.Fatalf("both runs got id %q", first.Project.ID)
	}
	// Ids end up in media/ and output/ paths, so they stay ASCII.
	for _, r := range first.Project.ID {
		if r > 127 {
			t.Fatalf("project id %q is not ASCII", first.Project.ID)
		}
	}
	if !strings.HasPrefix(first.Project.ID, "eat-well-") {
		t.Errorf("project id %q does not start with the title slug", first.Project.ID)
	}
}

func TestImportHonoursAnExplicitProjectID(t *testing.T) {
	engine := intake.New(intake.Options{Prober: &fixedProber{}})
	result, err := engine.Import(context.Background(), intake.Request{
		ProjectID: "longcanting", Title: "t", Script: "第一句话说的是这个。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Project.ID != "longcanting" {
		t.Errorf("got id %q, want longcanting", result.Project.ID)
	}
}

func TestImportRejectsAScriptWithoutNarration(t *testing.T) {
	engine := intake.New(intake.Options{Prober: &fixedProber{}})
	_, err := engine.Import(context.Background(), intake.Request{Title: "t", Script: "  \n "})
	if !errors.Is(err, intake.ErrEmptyScript) {
		t.Fatalf("got %v, want ErrEmptyScript", err)
	}
}

func TestImportRejectsAnUnmeasurablyShortLine(t *testing.T) {
	engine := intake.New(intake.Options{Prober: &fixedProber{durations: []int64{model.MinTargetMS - 1}}})
	_, err := engine.Import(context.Background(), intake.Request{Title: "t", Script: "第一句话说的是这个。"})
	if err == nil || !strings.Contains(err.Error(), "too short to budget") {
		t.Fatalf("got %v, want a too-short-to-budget error", err)
	}
}

func TestImportSurfacesProbeFailures(t *testing.T) {
	engine := intake.New(intake.Options{Prober: &fixedProber{err: errors.New("edge-tts is unreachable")}})
	_, err := engine.Import(context.Background(), intake.Request{Title: "t", Script: "第一句话说的是这个。"})
	if err == nil || !strings.Contains(err.Error(), "edge-tts is unreachable") {
		t.Fatalf("got %v, want the probe failure surfaced", err)
	}
}

func TestImportWithoutAProberIsRefused(t *testing.T) {
	_, err := intake.New(intake.Options{}).Import(context.Background(), intake.Request{Title: "t", Script: "第一句。"})
	if err == nil || !strings.Contains(err.Error(), "prober") {
		t.Fatalf("got %v, want a missing-prober error", err)
	}
}
