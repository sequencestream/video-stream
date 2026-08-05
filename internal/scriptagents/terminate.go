package scriptagents

// TerminationConfig holds the three termination numbers from the intent.
type TerminationConfig struct {
	// MaxRounds is the hard iteration ceiling.
	MaxRounds int `json:"max_rounds"`
	// MetricImprovementMin is the minimum score delta (fraction) that counts as progress.
	MetricImprovementMin float64 `json:"metric_improvement_min"`
	// MaxNewIssues is the maximum new critic issues allowed for early stop.
	MaxNewIssues int `json:"max_new_issues"`
	// StagnantRounds is how many consecutive low-improvement rounds trigger stop.
	StagnantRounds int `json:"stagnant_rounds"`
	// CostPer1KTokensMicros feeds the cost skill.
	CostPer1KTokensMicros int64 `json:"cost_per_1k_tokens_micros"`
}

// DefaultTermination returns the intent's written defaults.
func DefaultTermination() TerminationConfig {
	return TerminationConfig{
		MaxRounds:            3,
		MetricImprovementMin: 0.03,
		MaxNewIssues:         1, // <2 means at most 1 new issue
		StagnantRounds:       2,
		CostPer1KTokensMicros: 500,
	}
}

// RoundMetrics captures one polish round for termination decisions.
type RoundMetrics struct {
	Round      int     `json:"round"`
	Score      float64 `json:"score"`
	NewIssues  int     `json:"new_issues"`
	SkillsPass bool    `json:"skills_pass"`
}

// ShouldStop implements the termination rule from the intent.
func ShouldStop(cfg TerminationConfig, history []RoundMetrics) (bool, string) {
	if len(history) == 0 {
		return false, ""
	}
	last := history[len(history)-1]
	if last.Round >= cfg.MaxRounds {
		return true, "max_rounds"
	}
	if !last.SkillsPass {
		return false, ""
	}
	if len(history) < cfg.StagnantRounds {
		return false, ""
	}
	// Check last N rounds for stagnation and low new issues.
	stagnant := true
	start := len(history) - cfg.StagnantRounds
	if start < 1 {
		start = 1
	}
	for i := start; i < len(history); i++ {
		delta := history[i].Score - history[i-1].Score
		if delta >= cfg.MetricImprovementMin {
			stagnant = false
			break
		}
	}
	if stagnant && last.NewIssues <= cfg.MaxNewIssues {
		return true, "stagnant_low_issues"
	}
	return false, ""
}

// ImprovementBetween returns score delta between two rounds.
func ImprovementBetween(prev, cur RoundMetrics) float64 {
	return cur.Score - prev.Score
}
