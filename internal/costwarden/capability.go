package costwarden

// Tier names supplier quality bands.
type Tier string

const (
	TierPremium  Tier = "premium"
	TierStandard Tier = "standard"
	TierEconomy  Tier = "economy"
)

// ServiceKind classifies a billable capability.
type ServiceKind string

const (
	ServiceAIVideo ServiceKind = "ai_video"
	ServiceTTS     ServiceKind = "tts"
	ServiceLLM     ServiceKind = "llm"
	ServiceStock   ServiceKind = "stock"
	ServiceRender  ServiceKind = "render"
)

// Capability describes one supplier offering used for cost lookup.
type Capability struct {
	ID                string      `json:"id"`
	Supplier          string      `json:"supplier"`
	Tier              Tier        `json:"tier"`
	Service           ServiceKind `json:"service"`
	CostPerUnitMicros int64       `json:"cost_per_unit_micros"`
	Unit              string      `json:"unit"`
	Available         bool        `json:"available"`
	RateLimitRPM      int         `json:"rate_limit_rpm,omitempty"`
}

// DefaultCapabilities returns the MVP supplier catalog.
func DefaultCapabilities() []Capability {
	return []Capability{
		{ID: "openai-video-premium", Supplier: "openai", Tier: TierPremium, Service: ServiceAIVideo, CostPerUnitMicros: 350_000, Unit: "per_shot", Available: true, RateLimitRPM: 10},
		{ID: "dashscope-video-premium", Supplier: "dashscope", Tier: TierPremium, Service: ServiceAIVideo, CostPerUnitMicros: 320_000, Unit: "per_shot", Available: true, RateLimitRPM: 12},
		{ID: "openai-video-standard", Supplier: "openai", Tier: TierStandard, Service: ServiceAIVideo, CostPerUnitMicros: 220_000, Unit: "per_shot", Available: true, RateLimitRPM: 20},
		{ID: "dashscope-video-standard", Supplier: "dashscope", Tier: TierStandard, Service: ServiceAIVideo, CostPerUnitMicros: 200_000, Unit: "per_shot", Available: true, RateLimitRPM: 24},
		{ID: "openai-video-economy", Supplier: "openai", Tier: TierEconomy, Service: ServiceAIVideo, CostPerUnitMicros: 120_000, Unit: "per_shot", Available: true, RateLimitRPM: 30},
		{ID: "dashscope-video-economy", Supplier: "dashscope", Tier: TierEconomy, Service: ServiceAIVideo, CostPerUnitMicros: 100_000, Unit: "per_shot", Available: false, RateLimitRPM: 30},
		{ID: "azure-tts-standard", Supplier: "azure", Tier: TierStandard, Service: ServiceTTS, CostPerUnitMicros: 12_000, Unit: "per_seg", Available: true},
		{ID: "openai-llm-standard", Supplier: "openai", Tier: TierStandard, Service: ServiceLLM, CostPerUnitMicros: 50_000, Unit: "per_run", Available: true},
		{ID: "stock-pond5", Supplier: "pond5", Tier: TierStandard, Service: ServiceStock, CostPerUnitMicros: 40_000, Unit: "per_shot", Available: true},
		{ID: "render-ffmpeg", Supplier: "local", Tier: TierEconomy, Service: ServiceRender, CostPerUnitMicros: 30_000, Unit: "per_run", Available: true},
	}
}

// Catalog indexes capabilities for lookup and failover.
type Catalog struct {
	items []Capability
}

// NewCatalog builds a catalog from capabilities.
func NewCatalog(caps ...Capability) *Catalog {
	if len(caps) == 0 {
		caps = DefaultCapabilities()
	}
	return &Catalog{items: caps}
}

// Capabilities returns a copy of all entries.
func (c *Catalog) Capabilities() []Capability {
	out := make([]Capability, len(c.items))
	copy(out, c.items)
	return out
}

// SetAvailable toggles a capability (for tests / runtime health).
func (c *Catalog) SetAvailable(id string, available bool) {
	for i := range c.items {
		if c.items[i].ID == id {
			c.items[i].Available = available
			return
		}
	}
}

// AIVideo picks the cheapest available capability for tier/supplier preference.
func (c *Catalog) AIVideo(tier Tier, supplier string) (Capability, bool) {
	var best *Capability
	for i := range c.items {
		cap := &c.items[i]
		if cap.Service != ServiceAIVideo || !cap.Available {
			continue
		}
		if tier != "" && cap.Tier != tier {
			continue
		}
		if supplier != "" && cap.Supplier != supplier {
			continue
		}
		if best == nil || cap.CostPerUnitMicros < best.CostPerUnitMicros {
			best = cap
		}
	}
	if best == nil {
		return Capability{}, false
	}
	return *best, true
}

// FailoverSameTier returns another available ai_video capability at the same tier.
func (c *Catalog) FailoverSameTier(current Capability) (Capability, bool) {
	var best *Capability
	for i := range c.items {
		cap := &c.items[i]
		if cap.Service != ServiceAIVideo || !cap.Available || cap.ID == current.ID {
			continue
		}
		if cap.Tier != current.Tier {
			continue
		}
		if best == nil || cap.CostPerUnitMicros < best.CostPerUnitMicros {
			best = cap
		}
	}
	if best == nil {
		return Capability{}, false
	}
	return *best, true
}

// TTSCost returns per-seg TTS micros.
func (c *Catalog) TTSCost() int64 {
	for _, cap := range c.items {
		if cap.Service == ServiceTTS && cap.Available {
			return cap.CostPerUnitMicros
		}
	}
	return 10_000
}

// RouteCostMicros maps a hybrid route to unit cost.
func RouteCostMicros(route string, ai Capability) int64 {
	switch route {
	case "ai_video":
		return ai.CostPerUnitMicros
	case "stock_footage":
		return 40_000
	case "ken_burns_still":
		return 15_000
	case "motion_graphics":
		return 8_000
	default:
		return 20_000
	}
}
