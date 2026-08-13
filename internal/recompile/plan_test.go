package recompile_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
	"github.com/sequencestream/video-stream/internal/recompile"
)

func planProject() model.Project {
	p := model.NewProject("plan-project", "plan", time.Now())
	p.Segs = []model.Seg{
		model.NewSeg("a", "first", 2000),
		model.NewSeg("b", "second", 2000),
	}
	p.Seal()
	return p
}

func TestPlanValidateForRejectsUnsafeExecutionContracts(t *testing.T) {
	project := planProject()
	tests := []struct {
		name string
		plan recompile.Plan
		want string
	}{
		{"wrong project", recompile.Plan{ProjectID: "other", Invalidated: []string{"a", "b"}}, "belongs to project"},
		{"unknown seg", recompile.Plan{ProjectID: project.ID, Invalidated: []string{"a", "missing"}}, "unknown seg"},
		{"duplicate seg", recompile.Plan{ProjectID: project.ID, Invalidated: []string{"a"}, Reused: []string{"a", "b"}}, "appears in both"},
		{"omitted seg", recompile.Plan{ProjectID: project.ID, Invalidated: []string{"a"}}, "omits seg"},
		{"boundary without full rerun", recompile.Plan{ProjectID: project.ID, Invalidated: []string{"a", "b"}, Boundary: recompile.BoundaryHookEdited}, "unexpectedly names boundary"},
		{"full rerun without boundary", recompile.Plan{ProjectID: project.ID, Invalidated: []string{"a", "b"}, FullRerun: true}, "has no boundary"},
		{"full rerun with reuse", recompile.Plan{ProjectID: project.ID, Invalidated: []string{"a"}, Reused: []string{"b"}, FullRerun: true, Boundary: recompile.BoundaryHookEdited}, "invalidate all"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.plan.ValidateFor(project); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateFor() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPlanValidateForAcceptsIncrementalAndFullPlans(t *testing.T) {
	project := planProject()
	for _, plan := range []recompile.Plan{
		{ProjectID: project.ID, Invalidated: []string{"a"}, Reused: []string{"b"}},
		{ProjectID: project.ID, Invalidated: []string{"a", "b"}, FullRerun: true, Boundary: recompile.BoundaryHookEdited},
	} {
		if err := plan.ValidateFor(project); err != nil {
			t.Fatalf("ValidateFor(%+v): %v", plan, err)
		}
	}
}
