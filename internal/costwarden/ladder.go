package costwarden

import "github.com/sequencestream/video-stream/internal/hybrid"

// Level is a degradation step (0 = original plan).
type Level int

const (
	LevelOriginal            Level = 0
	LevelSwitchSupplier      Level = 1
	LevelDowngradeTier       Level = 2
	LevelDowngradeResolution Level = 3
	LevelReduceShots         Level = 4
	LevelKenBurns            Level = 5
	LevelStock               Level = 6
	LevelMotionGraphics      Level = 7
)

// LevelAction describes one ladder rung.
type LevelAction struct {
	Level  Level  `json:"level"`
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// LadderActions returns ordered degradation steps.
func LadderActions() []LevelAction {
	return []LevelAction{
		{Level: LevelSwitchSupplier, Action: "switch_supplier_same_tier", Reason: "swap to an available same-tier supplier"},
		{Level: LevelDowngradeTier, Action: "downgrade_tier", Reason: "move from premium to standard to economy"},
		{Level: LevelDowngradeResolution, Action: "downgrade_resolution", Reason: "render at 720p instead of 1080p"},
		{Level: LevelReduceShots, Action: "reduce_ai_shots", Reason: "drop AI video shots to zero"},
		{Level: LevelKenBurns, Action: "ken_burns_still", Reason: "replace expensive routes with Ken Burns stills"},
		{Level: LevelStock, Action: "stock_footage", Reason: "use licensed stock for all visuals"},
		{Level: LevelMotionGraphics, Action: "motion_graphics_only", Reason: "cheapest motion-graphics-only path"},
	}
}

// ApplyLevel mutates state for one degradation level.
func ApplyLevel(state *PlanState, level Level, cat *Catalog) bool {
	switch level {
	case LevelSwitchSupplier:
		next, ok := cat.FailoverSameTier(state.AICapability)
		if !ok {
			return false
		}
		state.AICapability = next
		return true
	case LevelDowngradeTier:
		switch state.Tier {
		case TierPremium:
			state.Tier = TierStandard
		case TierStandard:
			state.Tier = TierEconomy
		default:
			return false
		}
		cap, ok := cat.AIVideo(state.Tier, "")
		if ok {
			state.AICapability = cap
		}
		return ok
	case LevelDowngradeResolution:
		if state.Resolution == "720p" {
			return false
		}
		state.Resolution = "720p"
		return true
	case LevelReduceShots:
		if state.MaxAIShots <= 0 {
			return false
		}
		state.MaxAIShots = 0
		return true
	case LevelKenBurns:
		state.ForceMinRoute = hybrid.RouteKenBurnsStill
		return true
	case LevelStock:
		state.ForceMinRoute = hybrid.RouteStockFootage
		return true
	case LevelMotionGraphics:
		state.ForceMinRoute = hybrid.RouteMotionGraphics
		return true
	default:
		return false
	}
}
