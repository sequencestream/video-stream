package costwarden

import (
	"github.com/sequencestream/video-stream/internal/hybrid"
	"github.com/sequencestream/video-stream/internal/model"
)

// PlanState is the mutable pipeline configuration cost planning adjusts.
type PlanState struct {
	AICapability  Capability     `json:"ai_capability"`
	Tier          Tier           `json:"tier"`
	Resolution    string         `json:"resolution"` // 1080p | 720p
	MaxAIShots    int            `json:"max_ai_shots"`
	ForceMinRoute hybrid.Route   `json:"force_min_route,omitempty"`
}

// DefaultPlanState returns the MVP starting point.
func DefaultPlanState(cat *Catalog) PlanState {
	cap, ok := cat.AIVideo(TierPremium, "openai")
	if !ok {
		cap, _ = cat.AIVideo(TierPremium, "")
	}
	return PlanState{
		AICapability: cap,
		Tier:         TierPremium,
		Resolution:   "1080p",
		MaxAIShots:   hybrid.DefaultMaxAIShots,
	}
}

// EstimateInput carries a project and optional polish overhead.
type EstimateInput struct {
	Project          model.Project
	State            PlanState
	ScriptCostMicros int64
}

// EstimateBreakdown itemizes predicted spend.
type EstimateBreakdown struct {
	VisualMicros int64            `json:"visual_micros"`
	TTSMicros    int64            `json:"tts_micros"`
	ScriptMicros int64            `json:"script_micros"`
	RenderMicros int64            `json:"render_micros"`
	PerSeg       map[string]int64 `json:"per_seg,omitempty"`
	TotalMicros  int64            `json:"total_micros"`
}

// Estimate computes predicted cost for a project at the given plan state.
func Estimate(in EstimateInput, cat *Catalog) (EstimateBreakdown, []hybrid.ShotPlan, error) {
	if len(in.Project.Segs) == 0 {
		return EstimateBreakdown{}, nil, ErrEmptyProject
	}
	if cat == nil {
		cat = NewCatalog()
	}
	state := in.State
	if state.MaxAIShots <= 0 {
		state.MaxAIShots = hybrid.DefaultMaxAIShots
	}
	cfg := hybrid.Config{MaxAIShots: state.MaxAIShots, VideoDurationMS: hybrid.DefaultVideoDurationMS}
	plans, err := hybrid.Plan(in.Project.Segs, cfg)
	if err != nil {
		return EstimateBreakdown{}, nil, err
	}
	plans = applyRouteFloor(plans, state.ForceMinRoute)

	resScale := 1.0
	if state.Resolution == "720p" {
		resScale = 0.65
	}

	out := EstimateBreakdown{PerSeg: make(map[string]int64, len(plans))}
	for _, p := range plans {
		cost := int64(float64(RouteCostMicros(string(p.Route), state.AICapability)) * resScale)
		out.VisualMicros += cost
		out.PerSeg[p.SegID] = cost
	}
	out.TTSMicros = cat.TTSCost() * int64(len(in.Project.Segs))
	out.ScriptMicros = in.ScriptCostMicros
	for _, cap := range cat.items {
		if cap.Service == ServiceRender && cap.Available {
			out.RenderMicros = cap.CostPerUnitMicros
			break
		}
	}
	out.TotalMicros = out.VisualMicros + out.TTSMicros + out.ScriptMicros + out.RenderMicros
	return out, plans, nil
}

func applyRouteFloor(plans []hybrid.ShotPlan, floor hybrid.Route) []hybrid.ShotPlan {
	if floor == "" {
		return plans
	}
	out := make([]hybrid.ShotPlan, len(plans))
	for i, p := range plans {
		out[i] = p
		if routeRank(p.Route) > routeRank(floor) {
			out[i].Route = floor
			out[i].Reason = "costwarden degradation floor: " + string(floor)
		}
	}
	return out
}

func routeRank(r hybrid.Route) int {
	switch r {
	case hybrid.RouteMotionGraphics:
		return 0
	case hybrid.RouteKenBurnsStill:
		return 1
	case hybrid.RouteStockFootage:
		return 2
	case hybrid.RouteAIVideo:
		return 3
	default:
		return 2
	}
}

// WithinTolerance reports whether actual is within EstimateTolerancePercent of estimate.
func WithinTolerance(estimated, actual int64) bool {
	if estimated <= 0 {
		return actual == 0
	}
	diff := estimated - actual
	if diff < 0 {
		diff = -diff
	}
	return diff*100 <= estimated*int64(EstimateTolerancePercent)
}
