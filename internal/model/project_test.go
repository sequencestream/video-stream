package model_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
)

func newTestProject(t *testing.T, segs ...model.Seg) model.Project {
	t.Helper()
	p := model.NewProject("p1", "test", time.Unix(0, 0))
	p.Segs = segs
	p.Seal()
	return p
}

func TestProjectValidateAcceptsASealedGraph(t *testing.T) {
	a := model.NewSeg("a", "first line", 1000)
	b := model.NewSeg("b", "second line", 1000)
	b.DependsOn = []string{"a"}

	p := newTestProject(t, a, b)
	if err := p.Validate(); err != nil {
		t.Fatalf("a sealed project was rejected: %v", err)
	}

	order, err := p.RenderOrder()
	if err != nil {
		t.Fatalf("RenderOrder: %v", err)
	}
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("got order %v, want [a b]", order)
	}
}

// Ties must be broken deterministically, otherwise the render order — and with
// it every cache decision — would depend on Go's map iteration.
func TestRenderOrderIsDeterministic(t *testing.T) {
	segs := []model.Seg{
		model.NewSeg("delta", "d", 1000),
		model.NewSeg("alpha", "a", 1000),
		model.NewSeg("charlie", "c", 1000),
		model.NewSeg("bravo", "b", 1000),
	}
	p := newTestProject(t, segs...)

	first, err := p.RenderOrder()
	if err != nil {
		t.Fatalf("RenderOrder: %v", err)
	}
	for i := 0; i < 50; i++ {
		again, err := p.RenderOrder()
		if err != nil {
			t.Fatalf("RenderOrder: %v", err)
		}
		for j := range first {
			if again[j] != first[j] {
				t.Fatalf("run %d returned %v, want %v", i, again, first)
			}
		}
	}
	want := []string{"alpha", "bravo", "charlie", "delta"}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("got %v, want %v", first, want)
		}
	}
}

// "A cycle exists" is useless in a graph of hundreds of segs, so the message
// has to name the path.
func TestDependencyCycleIsRejectedWithThePathInTheMessage(t *testing.T) {
	a := model.NewSeg("a", "first", 1000)
	a.DependsOn = []string{"c"}
	b := model.NewSeg("b", "second", 1000)
	b.DependsOn = []string{"a"}
	c := model.NewSeg("c", "third", 1000)
	c.DependsOn = []string{"b"}

	p := newTestProject(t, a, b, c)
	err := p.Validate()
	if !errors.Is(err, model.ErrDependencyCycle) {
		t.Fatalf("got %v, want ErrDependencyCycle", err)
	}
	for _, id := range []string{"a", "b", "c", "->"} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("error %q does not spell out the cycle path", err)
		}
	}
}

func TestSelfDependencyIsACycle(t *testing.T) {
	a := model.NewSeg("a", "first", 1000)
	a.DependsOn = []string{"a"}

	err := newTestProject(t, a).Validate()
	if !errors.Is(err, model.ErrDependencyCycle) {
		t.Fatalf("got %v, want ErrDependencyCycle", err)
	}
	if !strings.Contains(err.Error(), "a -> a") {
		t.Fatalf("error %q should show the self loop", err)
	}
}

func TestUnknownDependencyIsRejected(t *testing.T) {
	a := model.NewSeg("a", "first", 1000)
	a.DependsOn = []string{"ghost"}

	err := newTestProject(t, a).Validate()
	if !errors.Is(err, model.ErrUnknownDependency) {
		t.Fatalf("got %v, want ErrUnknownDependency", err)
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("error %q should name the missing seg", err)
	}
}

// A stale render cache key shows up as "I edited the script but the video did
// not change", which is the most expensive class of bug here to diagnose. It
// has to fail at validation instead.
func TestProjectValidateRejectsStaleDerivedFields(t *testing.T) {
	runs := []struct {
		name  string
		mutil func(*model.Project)
	}{
		{"text edited without re-sealing", func(p *model.Project) { p.Segs[0].Text = "edited after sealing" }},
		{"content hash hand-written", func(p *model.Project) { p.Segs[0].ContentHash = "ch1:made-up" }},
		{"render cache key hand-written", func(p *model.Project) { p.Segs[0].RenderCacheKey = "rk1:made-up" }},
		{"render profile swapped", func(p *model.Project) { p.RenderProfile = model.RenderProfile{Voice: "other"} }},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			p := newTestProject(t, model.NewSeg("a", "first line", 1000))
			run.mutil(&p)
			err := p.Validate()
			if !errors.Is(err, model.ErrStaleDerived) {
				t.Fatalf("got %v, want ErrStaleDerived", err)
			}
			if !strings.Contains(err.Error(), "Seal") {
				t.Fatalf("error %q should tell the caller to re-seal", err)
			}
		})
	}
}

func TestProjectValidateRejectsStructuralProblems(t *testing.T) {
	runs := []struct {
		name  string
		mutil func(*model.Project)
		want  string
	}{
		{"no segs", func(p *model.Project) { p.Segs = nil }, "at least one seg"},
		{"empty id", func(p *model.Project) { p.ID = "" }, "project.id"},
		{"wrong schema version", func(p *model.Project) { p.SchemaVersion = 99 }, "schema_version"},
		{"duplicate seg id", func(p *model.Project) { p.Segs = append(p.Segs, p.Segs[0]) }, "more than once"},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			p := newTestProject(t, model.NewSeg("a", "first line", 1000))
			run.mutil(&p)
			err := p.Validate()
			if err == nil {
				t.Fatalf("%s should have been rejected", run.name)
			}
			if !strings.Contains(err.Error(), run.want) {
				t.Fatalf("error %q does not mention %q", err, run.want)
			}
		})
	}
}

// RenderOrder is exported, so it can be reached on a project that never went
// through Validate. With a duplicate id the topological sort sees fewer nodes
// than segs and finishes with an incomplete order but no cycle to report, which
// used to index into an empty slice.
func TestRenderOrderRejectsDuplicateSegIDsInsteadOfPanicking(t *testing.T) {
	p := newTestProject(t, model.NewSeg("a", "first line", 1000))
	p.Segs = append(p.Segs, p.Segs[0])

	order, err := p.RenderOrder()
	if err == nil {
		t.Fatalf("duplicate seg ids were accepted, returning %v", order)
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("error %q should name the duplicate", err)
	}
}

func TestProjectValidateRejectsAnUtteranceAlignedToAnUnknownSeg(t *testing.T) {
	p := newTestProject(t, model.NewSeg("a", "first line", 1000))
	p.Timeline = model.Timeline{Events: []model.Event{{
		ID:   "e1",
		Kind: model.EventSpeech,
		Utterances: []model.Utterance{{
			ID:     "u1",
			SegID:  "ghost",
			Tokens: []model.Token{newToken("t1", "first", 0, 400)},
		}},
	}}}

	err := p.Validate()
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("got %v, want an error naming the unknown seg", err)
	}
}

func TestProjectSegLookup(t *testing.T) {
	p := newTestProject(t, model.NewSeg("a", "first line", 1000))
	if _, ok := p.Seg("a"); !ok {
		t.Fatal("seg a should be found")
	}
	if _, ok := p.Seg("b"); ok {
		t.Fatal("seg b should not be found")
	}
}
