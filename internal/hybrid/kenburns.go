package hybrid

import (
	"hash/fnv"
)

// KenBurnsParams describes a reproducible pan/zoom on a still.
type KenBurnsParams struct {
	Seed       uint64  `json:"seed"`
	StartScale float64 `json:"start_scale"`
	EndScale   float64 `json:"end_scale"`
	StartX     float64 `json:"start_x"`
	StartY     float64 `json:"start_y"`
	EndX       float64 `json:"end_x"`
	EndY       float64 `json:"end_y"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
}

// KenBurnsSeed derives a stable seed from seg identity and text.
func KenBurnsSeed(segID, text string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(segID + "\x00" + text))
	return h.Sum64()
}

// ComputeKenBurns returns deterministic motion params for a seed.
func ComputeKenBurns(seed uint64, width, height int) KenBurnsParams {
	s := seed
	next := func() float64 {
		s = s*6364136223846793005 + 1
		return float64(s>>33) / float64(1<<31)
	}
	startScale := 1.0 + next()*0.1
	endScale := startScale + 0.08 + next()*0.07
	return KenBurnsParams{
		Seed: seed, StartScale: startScale, EndScale: endScale,
		StartX: next() * 0.1, StartY: next() * 0.1,
		EndX: 0.05 + next()*0.1, EndY: 0.05 + next()*0.1,
		Width: width, Height: height,
	}
}

// Equal reports whether two Ken Burns params match.
func (k KenBurnsParams) Equal(other KenBurnsParams) bool {
	return k.Seed == other.Seed &&
		k.StartScale == other.StartScale && k.EndScale == other.EndScale &&
		k.StartX == other.StartX && k.StartY == other.StartY &&
		k.EndX == other.EndX && k.EndY == other.EndY
}
