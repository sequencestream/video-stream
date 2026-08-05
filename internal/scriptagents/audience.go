package scriptagents

import (
	"context"
	"fmt"
	"strings"
)

// DropOff is one audience departure moment. Only second + reason — no judgement.
type DropOff struct {
	Second int    `json:"second"`
	Reason string `json:"reason"`
}

// AudienceReport holds drop-offs at the three fixed moments.
type AudienceReport struct {
	DropOffs []DropOff `json:"drop_offs"`
}

var judgementWords = []string{
	"good", "bad", "better", "worse", "great", "terrible", "excellent", "poor",
	"strong", "weak", "boring", "engaging", "好", "差", "棒", "烂",
}

// Validate ensures the report contains no quality judgement.
func (r AudienceReport) Validate() error {
	for _, d := range r.DropOffs {
		lower := strings.ToLower(d.Reason)
		for _, w := range judgementWords {
			if strings.Contains(lower, w) {
				return fmt.Errorf("%w: reason at %ds contains %q", ErrAudienceJudged, d.Second, w)
			}
		}
		if d.Second <= 0 || strings.TrimSpace(d.Reason) == "" {
			return fmt.Errorf("drop-off at second %d must have a non-empty reason", d.Second)
		}
	}
	return nil
}

// AudienceSimulator predicts where viewers leave without judging quality.
type AudienceSimulator interface {
	Simulate(ctx context.Context, d Draft) (AudienceReport, error)
}

// RuleAudienceSimulator is deterministic for tests.
type RuleAudienceSimulator struct{}

// Simulate implements AudienceSimulator.
func (RuleAudienceSimulator) Simulate(_ context.Context, d Draft) (AudienceReport, error) {
	reasons := map[Direction][3]string{
		DirectionQuestion: {
			"hook promise unclear before payoff label appears",
			"density spike before context lands",
			"payoff delayed after setup loop",
		},
		DirectionStory: {
			"opening lacks concrete anchor",
			"timeline jump without marker",
			"closing lacks actionable frame",
		},
		DirectionContrarian: {
			"contrarian claim needs one beat earlier",
			"middle section repeats hook rhythm",
			"final beat mirrors opening too closely",
		},
	}
	rs, ok := reasons[d.Direction]
	if !ok {
		rs = reasons[DirectionQuestion]
	}
	report := AudienceReport{
		DropOffs: []DropOff{
			{Second: DropOffSecondHook, Reason: rs[0]},
			{Second: DropOffSecondContext, Reason: rs[1]},
			{Second: DropOffSecondPayoff, Reason: rs[2]},
		},
	}
	return report, report.Validate()
}
