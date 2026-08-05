package hybrid

import "github.com/sequencestream/video-stream/internal/model"

// LabeledFixture is one golden test case for route decision.
type LabeledFixture struct {
	Name     string
	Input    ShotInput
	Want     Route
	AIBudget int
}

// GoldenFixtures is the labeled test set from the intent acceptance criteria.
var GoldenFixtures = []LabeledFixture{
	{
		Name: "hook_with_consistency_uses_ai",
		Input: ShotInput{
			SegID: "hook", Ordinal: 0, Text: "Stop scrolling — this changes everything.",
			EmotionTag: model.EmotionUrgent, ContinuityGroup: "hero",
		},
		Want: RouteAIVideo, AIBudget: 1,
	},
	{
		Name: "data_segment_uses_stock",
		Input: ShotInput{
			SegID: "body-1", Text: "Our survey shows 73% of creators quit after week two.",
		},
		Want: RouteStockFootage, AIBudget: 1,
	},
	{
		Name: "dense_stats_use_motion_graphics",
		Input: ShotInput{
			SegID: "body-2", Text: "2024 2025 2026 40% 50% 60% growth rate data statistics report",
		},
		Want: RouteMotionGraphics, AIBudget: 1,
	},
	{
		Name: "filler_uses_ken_burns",
		Input: ShotInput{
			SegID: "body-3", Text: "Here is the simple takeaway for your channel.",
		},
		Want: RouteKenBurnsStill, AIBudget: 1,
	},
}

// MatchFixture checks DecideRoute against labeled fixtures.
func MatchFixture(f LabeledFixture) bool {
	got := DecideRoute(f.Input, DefaultConfig(), f.AIBudget)
	return got.Route == f.Want
}
