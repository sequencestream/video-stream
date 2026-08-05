package recompile

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sequencestream/video-stream/internal/model"
)

// detectBoundary returns the first boundary the edit crosses.
//
// The order is fixed and documented rather than incidental: project-wide causes
// are checked before seg-local ones, because when a style change also shifts
// the runtime, "you changed the style" is the cause and the drift is its
// symptom. Reporting whichever check happened to be written first would make
// the explanation depend on the source layout.
func detectBoundary(previous, next model.Project, prevOrder, nextOrder []string, changed map[string]struct{}) (Boundary, string) {
	if b, reason := styleAnchorBoundary(previous, next); b != BoundaryNone {
		return b, reason
	}
	if b, reason := durationDriftBoundary(previous, next); b != BoundaryNone {
		return b, reason
	}
	if b, reason := beatBoundary(previous, next, prevOrder, nextOrder); b != BoundaryNone {
		return b, reason
	}
	if b, reason := hookBoundary(previous, next, prevOrder, nextOrder); b != BoundaryNone {
		return b, reason
	}
	if b, reason := groupBoundary(previous, next, changed,
		BoundaryContinuityBroken, "continuity_group", continuityGroupOf,
		"the shots in it are one continuous action, so re-rendering part of it shows as a jump",
	); b != BoundaryNone {
		return b, reason
	}
	return groupBoundary(previous, next, changed,
		BoundaryBatchBroken, "generation_batch", generationBatchOf,
		"the shots in it were generated in one call conditioned on each other, so one re-rendered alone no longer matches",
	)
}

func styleAnchorBoundary(previous, next model.Project) (Boundary, string) {
	before, after := previous.RenderProfile.StyleAnchor, next.RenderProfile.StyleAnchor
	if before == after {
		return BoundaryNone, ""
	}
	return BoundaryStyleAnchor, fmt.Sprintf(
		"style anchor changed from %q to %q; every frame in the project was rendered under the old visual identity",
		before, after)
}

// durationDriftBoundary compares total runtime budgets.
//
// The comparison is integer-only, matching model.DurationBudget.Validate: a
// threshold that drifts with the platform's float rounding would make the same
// edit reusable on one machine and not on another.
func durationDriftBoundary(previous, next model.Project) (Boundary, string) {
	before, after := totalTargetMS(previous), totalTargetMS(next)
	if before == 0 {
		return BoundaryNone, ""
	}
	drift := after - before
	if drift < 0 {
		drift = -drift
	}
	if drift*100 <= DurationDriftPercent*before {
		return BoundaryNone, ""
	}
	return BoundaryDurationDrift, fmt.Sprintf(
		"total runtime moved from %dms to %dms, past the %d%% limit; shot lengths and pacing were planned against the old runtime",
		before, after, DurationDriftPercent)
}

// beatBoundary compares the emotional arc.
//
// The arc is the emotion_tag sequence read along render order. Comparing the
// sequence rather than per-seg tags is the point: swapping two segs leaves
// every tag intact and still re-lays out the whole arc.
func beatBoundary(previous, next model.Project, prevOrder, nextOrder []string) (Boundary, string) {
	before, after := beatSequence(previous, prevOrder), beatSequence(next, nextOrder)
	if slices.Equal(before, after) {
		return BoundaryNone, ""
	}
	return BoundaryBeatReordered, fmt.Sprintf(
		"emotional beats changed from [%s] to [%s]; the arc is re-laid out rather than revised at one point",
		joinTags(before), joinTags(after))
}

// hookBoundary reports an edit to the opening seg.
//
// The hook is the first seg in render order; it is not a marked field, because
// "the thing that plays first" is already determined by the graph and a second
// source of truth for it could disagree with the graph.
func hookBoundary(previous, next model.Project, prevOrder, nextOrder []string) (Boundary, string) {
	if len(prevOrder) == 0 || len(nextOrder) == 0 {
		return BoundaryNone, ""
	}
	before, after := prevOrder[0], nextOrder[0]
	if before != after {
		return BoundaryHookEdited, fmt.Sprintf(
			"the opening seg changed from %s to %s; every later shot is cut against the pace the hook sets",
			before, after)
	}

	prevSeg, okPrev := previous.Seg(before)
	nextSeg, okNext := next.Seg(after)
	if !okPrev || !okNext || prevSeg.ContentHash == nextSeg.ContentHash {
		return BoundaryNone, ""
	}
	return BoundaryHookEdited, fmt.Sprintf(
		"the opening seg %s was rewritten; every later shot is cut against the pace the hook sets", after)
}

// groupBoundary reports an edit that lands inside a group of segs that have to
// be produced together, or that changes who is in such a group.
//
// Both halves are needed. Editing a member is the obvious case; moving a seg
// between groups changes nothing about the seg's own content, so it never
// reaches the changed set, yet it re-partitions exactly the shots this check
// exists to keep together.
func groupBoundary(
	previous, next model.Project,
	changed map[string]struct{},
	boundary Boundary,
	field string,
	groupOf func(model.Seg) string,
	why string,
) (Boundary, string) {
	for _, segID := range slices.Sorted(maps.Keys(changed)) {
		seg, ok := next.Seg(segID)
		if !ok {
			continue
		}
		if group := groupOf(seg); group != "" {
			return boundary, fmt.Sprintf("seg %s is in %s %q and was edited; %s", segID, field, group, why)
		}
	}

	before, after := groupMembers(previous, groupOf), groupMembers(next, groupOf)
	if maps.EqualFunc(before, after, slices.Equal) {
		return BoundaryNone, ""
	}
	for _, group := range slices.Sorted(maps.Keys(mergedKeys(before, after))) {
		if slices.Equal(before[group], after[group]) {
			continue
		}
		return boundary, fmt.Sprintf("%s %q went from [%s] to [%s]; %s",
			field, group, strings.Join(before[group], " "), strings.Join(after[group], " "), why)
	}
	return BoundaryNone, ""
}

func continuityGroupOf(s model.Seg) string { return s.ContinuityGroup }
func generationBatchOf(s model.Seg) string { return s.GenerationBatch }

func totalTargetMS(p model.Project) int64 {
	var total int64
	for _, s := range p.Segs {
		total += s.DurationBudget.TargetMS()
	}
	return total
}

func beatSequence(p model.Project, order []string) []model.EmotionTag {
	tags := make([]model.EmotionTag, 0, len(order))
	for _, segID := range order {
		if seg, ok := p.Seg(segID); ok {
			tags = append(tags, seg.EmotionTag)
		}
	}
	return tags
}

func joinTags(tags []model.EmotionTag) string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = string(t)
	}
	return strings.Join(out, " ")
}

// groupMembers maps each non-empty group name to its sorted members. Segs
// outside any group are omitted rather than collected under "", which would
// make every unrelated edit look like a group change.
func groupMembers(p model.Project, groupOf func(model.Seg) string) map[string][]string {
	out := map[string][]string{}
	for _, s := range p.Segs {
		if group := groupOf(s); group != "" {
			out[group] = append(out[group], s.SegID)
		}
	}
	for group := range out {
		slices.Sort(out[group])
	}
	return out
}

func mergedKeys(a, b map[string][]string) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}
