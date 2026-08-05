package compliance

import "fmt"

// CheckReuseGate enforces per-account structure card reuse limits.
func CheckReuseGate(cfg Config, reuseCount int) GateResult {
	const gate = "reuse_frequency"
	cfg = cfg.Effective()
	metric := fmt.Sprintf("count=%d window_days=%d max=%d", reuseCount, cfg.ReuseWindowDays, cfg.MaxReuses)
	if reuseCount >= cfg.MaxReuses {
		return GateResult{
			Gate: gate, Passed: false, Metric: metric,
			Reason: fmt.Sprintf("structure card reused %d times in %d days (limit %d)", reuseCount, cfg.ReuseWindowDays, cfg.MaxReuses),
			Advice: "pick a different structure card or wait for the window to roll; do not republish the same skeleton",
		}
	}
	return GateResult{Gate: gate, Passed: true, Metric: metric}
}
