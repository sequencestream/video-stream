package compliance

import (
	"fmt"
	"math"

	"github.com/sequencestream/video-stream/internal/ideation"
)

// Config holds gate thresholds. Values may tighten but not loosen below floors.
type Config struct {
	RejectSimilarity float64 `json:"reject_similarity"`
	PassSimilarity   float64 `json:"pass_similarity"`
	ReuseWindowDays  int     `json:"reuse_window_days"`
	MaxReuses        int     `json:"max_reuses"`
}

// DefaultConfig returns intent defaults clamped to floors.
func DefaultConfig() Config {
	return Config{
		RejectSimilarity: FloorRejectSimilarity,
		PassSimilarity:   FloorPassSimilarity,
		ReuseWindowDays:  FloorReuseWindowDays,
		MaxReuses:        FloorMaxReuses,
	}
}

// Validate clamps config to floors and rejects bypass attempts.
func (c Config) Validate() error {
	if c.RejectSimilarity < FloorRejectSimilarity {
		return fmt.Errorf("%w: reject_similarity below floor %g", ErrBypassAttempt, FloorRejectSimilarity)
	}
	if c.PassSimilarity > FloorPassSimilarity {
		return fmt.Errorf("%w: pass_similarity above floor %g", ErrBypassAttempt, FloorPassSimilarity)
	}
	if c.ReuseWindowDays < FloorReuseWindowDays {
		return fmt.Errorf("%w: reuse_window_days below floor %d", ErrBypassAttempt, FloorReuseWindowDays)
	}
	if c.MaxReuses > FloorMaxReuses {
		return fmt.Errorf("%w: max_reuses above floor %d", ErrBypassAttempt, FloorMaxReuses)
	}
	return nil
}

// Effective returns config with zero values filled from defaults.
func (c Config) Effective() Config {
	d := DefaultConfig()
	if c.RejectSimilarity > 0 {
		d.RejectSimilarity = c.RejectSimilarity
	}
	if c.PassSimilarity > 0 {
		d.PassSimilarity = c.PassSimilarity
	}
	if c.ReuseWindowDays > 0 {
		d.ReuseWindowDays = c.ReuseWindowDays
	}
	if c.MaxReuses > 0 {
		d.MaxReuses = c.MaxReuses
	}
	return d
}

// CheckFingerprintGate compares candidate against prior fingerprints.
func CheckFingerprintGate(cfg Config, candidate []float64, prior [][]float64) GateResult {
	const gate = "structure_fingerprint"
	if len(candidate) == 0 {
		return GateResult{Gate: gate, Passed: false, Reason: "missing structure fingerprint",
			Advice: "provide a fingerprint vector derived from the script structure"}
	}
	cfg = cfg.Effective()
	var maxSim float64
	for _, p := range prior {
		sim := ideation.CosineSimilarity(candidate, p)
		if sim > maxSim {
			maxSim = sim
		}
	}
	metric := fmt.Sprintf("cosine=%.3f", maxSim)
	if maxSim > cfg.RejectSimilarity {
		return GateResult{
			Gate: gate, Passed: false, Metric: metric,
			Reason: fmt.Sprintf("structure too similar to a prior work (%.2f > %.2f)", maxSim, cfg.RejectSimilarity),
			Advice: fmt.Sprintf("mutate structure until similarity is ≤ %.2f — change hook pattern, beat order, or opening visual", cfg.PassSimilarity),
		}
	}
	if maxSim > cfg.PassSimilarity {
		return GateResult{
			Gate: gate, Passed: false, Metric: metric,
			Reason: fmt.Sprintf("structure not differentiated enough (%.2f > %.2f)", maxSim, cfg.PassSimilarity),
			Advice: "apply stronger structural mutation before reusing this template lineage",
		}
	}
	return GateResult{Gate: gate, Passed: true, Metric: metric}
}

// FingerprintFromLabels builds a fingerprint vector from structure labels.
func FingerprintFromLabels(labels ...string) []float64 {
	return ideation.EmbedFromCard(labels...)
}

// NormalizeFingerprint L2-normalises a vector copy.
func NormalizeFingerprint(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return v
	}
	n := math.Sqrt(sum)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}
