package visual

import (
	"fmt"
	"math"
	"strings"
)

// ShotPrompt is one rendered shot's visual prompt inputs for coherence testing.
type ShotPrompt struct {
	Palette         []string
	CompositionRule string
}

// CoherenceReport measures palette and composition consistency across shots.
type CoherenceReport struct {
	PaletteDistance      float64 `json:"palette_distance"`
	CompositionSimilarity float64 `json:"composition_similarity"`
	Passed               bool    `json:"passed"`
}

// MeasureCoherence checks that consecutive shots under one pack stay close.
func MeasureCoherence(pack StylePack, shots []ShotPrompt) (CoherenceReport, error) {
	if len(shots) < CoherenceShotCount {
		return CoherenceReport{}, fmt.Errorf("need %d shots, got %d", CoherenceShotCount, len(shots))
	}
	refPalette := normalizePalette(pack.Stack.Palette)
	refComp := pack.Stack.CompositionRule

	var maxDist float64
	var minSim float64 = 1
	for _, s := range shots[:CoherenceShotCount] {
		d := paletteDistance(refPalette, normalizePalette(s.Palette))
		if d > maxDist {
			maxDist = d
		}
		sim := compositionSimilarity(refComp, s.CompositionRule)
		if sim < minSim {
			minSim = sim
		}
	}
	report := CoherenceReport{
		PaletteDistance:       maxDist,
		CompositionSimilarity: minSim,
		Passed:                maxDist <= MaxPaletteDistance && minSim >= MinCompositionSimilarity,
	}
	return report, nil
}

func normalizePalette(colors []string) []float64 {
	out := make([]float64, 3)
	if len(colors) == 0 {
		return out
	}
	for i, c := range colors {
		if i >= 3 {
			break
		}
		out[i] = hexToUnit(c)
	}
	return out
}

func hexToUnit(hex string) float64 {
	hex = strings.TrimPrefix(strings.ToLower(hex), "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) != 6 {
		return 0
	}
	var v float64
	for i := 0; i < 6; i += 2 {
		var n int
		fmt.Sscanf(hex[i:i+2], "%x", &n)
		v += float64(n) / 255.0
	}
	return v / 3.0
}

func paletteDistance(a, b []float64) float64 {
	var sum float64
	for i := range a {
		d := a[i] - b[i]
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return sum / float64(len(a))
}

func compositionSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	// Token overlap Jaccard as a simple composition proxy.
	ta := tokenSet(a)
	tb := tokenSet(b)
	if len(ta) == 0 && len(tb) == 0 {
		return 1
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(s)) {
		t = strings.Trim(t, ",.;:")
		if t != "" {
			out[t] = true
		}
	}
	return out
}

// ShotsFromPack builds coherent shot prompts from one pack (for tests).
func ShotsFromPack(pack StylePack) []ShotPrompt {
	shots := make([]ShotPrompt, CoherenceShotCount)
	for i := range shots {
		shots[i] = ShotPrompt{
			Palette:         append([]string{}, pack.Stack.Palette...),
			CompositionRule: pack.Stack.CompositionRule,
		}
	}
	return shots
}

// MutateComposition breaks coherence for negative tests.
func MutateComposition(rule string) string {
	return rule + " dutch-angle"
}

// Clamp01 for tests.
func Clamp01(x float64) float64 {
	return math.Max(0, math.Min(1, x))
}
