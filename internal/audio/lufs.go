package audio

import "math"

// NormalizeLUFS adjusts a measured integrated loudness toward target within tolerance.
func NormalizeLUFS(measured, target, tolerance float64) (gainDB float64, ok bool) {
	delta := target - measured
	if math.Abs(delta) <= tolerance {
		return 0, true
	}
	return delta, math.Abs(delta) <= tolerance+0.01
}

// MeasureStub returns a deterministic pseudo-LUFS for stub audio paths.
func MeasureStub(uri string, target float64) float64 {
	if uri == "" {
		return target
	}
	// Bias slightly off target so normalization does something in tests.
	n := float64(len(uri)%5) - 2
	return target + n*0.3
}
