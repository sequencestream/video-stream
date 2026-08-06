package audio

import (
	"fmt"
	"math"

	"github.com/sequencestream/video-stream/internal/model"
)

// PlaybackRate picks a stretch factor so actualMS lands in budget, or rejects.
//
// rate > 1 speeds up (shorter output). Only ±MaxStretchPercent is allowed.
func PlaybackRate(actualMS int64, budget model.DurationBudget) (float64, error) {
	if actualMS <= 0 {
		return 0, fmt.Errorf("actual duration must be positive")
	}
	if budget.Contains(actualMS) {
		return 1.0, nil
	}
	minR := 1.0 - float64(MaxStretchPercent)/100.0
	maxR := 1.0 + float64(MaxStretchPercent)/100.0

	lo, hi := minR, maxR
	if actualMS > budget.MaxMS {
		lo = math.Max(lo, float64(actualMS)/float64(budget.MaxMS))
	}
	if actualMS < budget.MinMS {
		hi = math.Min(hi, float64(actualMS)/float64(budget.MinMS))
	}
	if lo > hi {
		return 0, ErrNeedsWordCountChange
	}

	rate := 1.0
	if rate < lo {
		rate = lo
	}
	if rate > hi {
		rate = hi
	}
	if !budget.Contains(AdjustedDurationMS(actualMS, rate)) {
		return 0, ErrNeedsWordCountChange
	}
	return rate, nil
}

// AdjustedDurationMS returns the output length after applying rate.
func AdjustedDurationMS(actualMS int64, rate float64) int64 {
	if rate <= 0 {
		return actualMS
	}
	return int64(math.Round(float64(actualMS) / rate))
}
