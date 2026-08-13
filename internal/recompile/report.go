package recompile

import (
	"github.com/sequencestream/video-stream/internal/store"
)

// ScrapThresholdPercent is the invalidation rate above which incremental
// recompilation should be admitted not to work.
//
// It is written down, in code, before there is any data, because a threshold
// chosen after seeing the numbers is not a threshold. Past it, the average edit
// re-renders most of the video, and everything the incremental path costs in
// complexity — the seg graph, the interval budget, the two hashes, the six
// boundaries — is being paid for a saving that is not there.
const ScrapThresholdPercent = 40

// MinRunsForVerdict is how many recorded runs a verdict needs.
//
// Without it the first edit that happens to cross a boundary reports a 100%
// invalidation rate and condemns the approach on a sample of one. Twenty is a
// judgement call, not a statistical result: it is roughly an afternoon of real
// editing, which is the smallest sample worth arguing about.
const MinRunsForVerdict = 20

// Verdict is the report's answer on whether incremental recompilation is
// earning its keep.
type Verdict string

// The three verdicts.
const (
	// VerdictInsufficientData means fewer than MinRunsForVerdict runs are on
	// record. It is a distinct value rather than a defaulted "viable" so that
	// nobody reads early silence as early success.
	VerdictInsufficientData Verdict = "insufficient_data"
	// VerdictViable means the invalidation rate is at or below the threshold.
	VerdictViable Verdict = "viable"
	// VerdictScrap means the rate is past ScrapThresholdPercent.
	VerdictScrap Verdict = "scrap"
)

// Report is the aggregate view over recorded runs.
//
// Its counters are seg totals rather than per-run averages; see
// InvalidationRate for why.
type Report struct {
	Runs             int              `json:"runs"`
	TotalSegs        int              `json:"total_segs"`
	InvalidatedSegs  int              `json:"invalidated_segs"`
	ReusedSegs       int              `json:"reused_segs"`
	FullRerunRuns    int              `json:"full_rerun_runs"`
	CostSavedMicros  int64            `json:"cost_saved_micros"`
	CacheHits        int              `json:"cache_hits"`
	RegeneratedSegs  int              `json:"regenerated_segs"`
	ElapsedMS        int64            `json:"elapsed_ms"`
	ActualCostMicros int64            `json:"actual_cost_micros"`
	ByBoundary       map[Boundary]int `json:"by_boundary,omitempty"`
}

// Aggregate folds recorded runs into a report.
//
// The arithmetic lives here rather than in SQL because how the rate is counted
// is the most contestable part of this feature, and it should be readable and
// unit-testable without a database in the way.
func Aggregate(runs []store.RecompileRun) Report {
	r := Report{Runs: len(runs)}
	for _, run := range runs {
		r.TotalSegs += run.TotalSegs
		r.InvalidatedSegs += run.InvalidatedSegs
		r.ReusedSegs += run.TotalSegs - run.InvalidatedSegs
		r.CostSavedMicros += run.CostSavedMicros
		r.CacheHits += run.CacheHits
		r.RegeneratedSegs += run.RegeneratedSegs
		r.ElapsedMS += run.ElapsedMS
		r.ActualCostMicros += run.ActualCostMicros
		if run.FullRerun {
			r.FullRerunRuns++
		}
		if run.Boundary != "" {
			if r.ByBoundary == nil {
				r.ByBoundary = map[Boundary]int{}
			}
			r.ByBoundary[Boundary(run.Boundary)]++
		}
	}
	return r
}

// InvalidationRate is invalidated segs over total segs across every run, in
// [0,1].
//
// It is seg-weighted, not the mean of each run's rate. Averaging per run gives
// a three-seg touch-up the same weight as a two-hundred-seg full rerun, so a
// stream of tiny edits would hide the cost of the rare expensive one — and the
// rare expensive one is what decides whether this is affordable.
//
// A full rerun contributes its whole seg count at full weight. There is no
// discount for "the user asked for something big": the number is meant to say
// what recompilation costs in practice, not to be defended.
func (r Report) InvalidationRate() float64 {
	if r.TotalSegs == 0 {
		return 0
	}
	return float64(r.InvalidatedSegs) / float64(r.TotalSegs)
}

// ReuseRate is the complement of InvalidationRate.
func (r Report) ReuseRate() float64 {
	if r.TotalSegs == 0 {
		return 0
	}
	return float64(r.ReusedSegs) / float64(r.TotalSegs)
}

// FullRerunRate is the share of runs a boundary forced, in [0,1]. A high value
// alongside an acceptable InvalidationRate points at the boundaries being drawn
// too wide rather than at the cache failing.
func (r Report) FullRerunRate() float64 {
	if r.Runs == 0 {
		return 0
	}
	return float64(r.FullRerunRuns) / float64(r.Runs)
}

// Verdict applies ScrapThresholdPercent to the recorded evidence.
func (r Report) Verdict() Verdict {
	if r.Runs < MinRunsForVerdict {
		return VerdictInsufficientData
	}
	// Integer comparison for the same reason the duration checks use one: a
	// verdict that flips on float rounding at exactly the threshold is a
	// verdict nobody can reproduce.
	if r.InvalidatedSegs*100 > ScrapThresholdPercent*r.TotalSegs {
		return VerdictScrap
	}
	return VerdictViable
}
