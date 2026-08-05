// Package hybrid plans mixed visual generation routes per shot.
//
// Full AI is expensive; full stock is generic. The MVP spends AI budget on the
// hook only and covers everything else with stock or Ken Burns on stills.
package hybrid

const (
	// DefaultMaxAIShots is the MVP cap on AI-generated shots in one video.
	DefaultMaxAIShots = 1
	// DefaultVideoDurationMS is the reference duration for the one-AI-shot rule.
	DefaultVideoDurationMS = 60_000
	// StockMaxRetries is how many times a stock fetch is attempted.
	StockMaxRetries = 3
)

// Route is how one seg's visuals are produced.
type Route string

const (
	RouteAIVideo        Route = "ai_video"
	RouteStockFootage   Route = "stock_footage"
	RouteKenBurnsStill  Route = "ken_burns_still"
	RouteMotionGraphics Route = "motion_graphics"
)
