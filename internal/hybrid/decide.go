package hybrid

import (
	"strings"
	"unicode/utf8"

	"github.com/sequencestream/video-stream/internal/model"
)

// ShotInput is one seg's signals for route planning.
type ShotInput struct {
	SegID           string
	Ordinal         int
	Text            string
	EmotionTag      model.EmotionTag
	ContinuityGroup string
	VisualSlot      string
	// LabeledRoute is set only in fixture tests.
	LabeledRoute Route
}

// ShotPlan is the chosen route for one seg.
type ShotPlan struct {
	SegID      string `json:"seg_id"`
	Route      Route  `json:"route"`
	Reason     string `json:"reason"`
	KenBurns   *KenBurnsParams `json:"ken_burns,omitempty"`
	StockQuery string `json:"stock_query,omitempty"`
}

// Config controls hybrid planning defaults.
type Config struct {
	MaxAIShots       int
	VideoDurationMS  int64
	HookSegID        string
}

// DefaultConfig returns MVP defaults.
func DefaultConfig() Config {
	return Config{MaxAIShots: DefaultMaxAIShots, VideoDurationMS: DefaultVideoDurationMS, HookSegID: "hook"}
}

// DecideRoute picks a route for one shot given context.
func DecideRoute(in ShotInput, cfg Config, aiBudgetRemaining int) ShotPlan {
	if cfg.MaxAIShots <= 0 {
		cfg.MaxAIShots = DefaultMaxAIShots
	}
	if cfg.HookSegID == "" {
		cfg.HookSegID = "hook"
	}

	density := informationDensity(in.Text)
	needsConsistency := in.ContinuityGroup != "" || in.VisualSlot != ""
	isHook := in.SegID == cfg.HookSegID || in.Ordinal == 0

	// Information-dense segments → motion graphics (near-zero cost).
	if density >= 0.35 {
		return ShotPlan{
			SegID: in.SegID, Route: RouteMotionGraphics,
			Reason: "high information density favors motion graphics over generative video",
			StockQuery: keywordsFromText(in.Text),
		}
	}

	if isDataTalk(in.Text) {
		return ShotPlan{
			SegID: in.SegID, Route: RouteStockFootage,
			Reason: "talking-head/data segment uses licensed stock to avoid AI cost",
			StockQuery: keywordsFromText(in.Text),
		}
	}

	// Emotional / suspense / subject action with consistency need → AI, but MVP caps count.
	if needsConsistency && isActionEmotion(in.EmotionTag) && aiBudgetRemaining > 0 && isHook {
		return ShotPlan{
			SegID: in.SegID, Route: RouteAIVideo,
			Reason: "hook shot with consistency requirement uses the single AI video budget",
		}
	}

	seed := KenBurnsSeed(in.SegID, in.Text)
	kb := ComputeKenBurns(seed, 1920, 1080)
	return ShotPlan{
		SegID: in.SegID, Route: RouteKenBurnsStill,
		Reason: "default MVP path: still frame with reproducible Ken Burns motion",
		KenBurns: &kb,
	}
}

// Plan builds routes for all segs in a project document.
func Plan(segs []model.Seg, cfg Config) ([]ShotPlan, error) {
	if cfg.MaxAIShots <= 0 {
		cfg = DefaultConfig()
	}
	inputs := make([]ShotInput, len(segs))
	for i, s := range segs {
		inputs[i] = ShotInput{
			SegID: s.SegID, Ordinal: i, Text: s.Text,
			EmotionTag: s.EmotionTag, ContinuityGroup: s.ContinuityGroup,
			VisualSlot: s.VisualPromptSlot,
		}
	}
	aiLeft := cfg.MaxAIShots
	plans := make([]ShotPlan, 0, len(inputs))
	for _, in := range inputs {
		p := DecideRoute(in, cfg, aiLeft)
		if p.Route == RouteAIVideo {
			aiLeft--
		}
		plans = append(plans, p)
	}
	return plans, nil
}

func informationDensity(text string) float64 {
	if text == "" {
		return 0
	}
	n := utf8.RuneCountInString(text)
	digits, words := 0, 0
	for _, w := range strings.Fields(text) {
		words++
		for _, r := range w {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
	}
	if words == 0 {
		return 0
	}
	return float64(digits)/float64(n) + float64(words)/float64(n)*0.5
}

func isActionEmotion(e model.EmotionTag) bool {
	switch e {
	case model.EmotionExcited, model.EmotionUrgent, model.EmotionSerious:
		return true
	default:
		return false
	}
}

func isDataTalk(text string) bool {
	lower := strings.ToLower(text)
	for _, w := range []string{"percent", "%", "data", "survey", "statistics", "数据", "统计", "报告"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

func keywordsFromText(text string) string {
	words := strings.Fields(text)
	if len(words) > 4 {
		words = words[:4]
	}
	return strings.Join(words, " ")
}

// CountAIRoutes returns how many shots use AI video.
func CountAIRoutes(plans []ShotPlan) int {
	n := 0
	for _, p := range plans {
		if p.Route == RouteAIVideo {
			n++
		}
	}
	return n
}
