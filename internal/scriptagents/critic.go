package scriptagents

import (
	"fmt"
	"regexp"
	"strings"
)

// CriticIssue is one diagnosed problem with evidence location.
type CriticIssue struct {
	SegID    string `json:"seg_id"`
	Problem  string `json:"problem"`
	Evidence string `json:"evidence"`
}

// CriticFeedback is diagnose-only output from the critic pass.
type CriticFeedback struct {
	Issues []CriticIssue `json:"issues"`
}

var rewritePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(instead (write|use|say))\b`),
	regexp.MustCompile(`(?i)\b(replace with|change to|rewrite as)\b`),
	regexp.MustCompile(`(?i)\b(should be:|try this:)\b`),
	regexp.MustCompile(`(?i)「[^」]+」`), // quoted replacement in CJK quotes
}

// Validate rejects feedback that contains rewrite prescriptions.
func (f CriticFeedback) Validate() error {
	for _, issue := range f.Issues {
		text := issue.Problem + " " + issue.Evidence
		for _, re := range rewritePatterns {
			if re.MatchString(text) {
				return fmt.Errorf("%w: issue on seg %s", ErrCriticRewrote, issue.SegID)
			}
		}
		if strings.Contains(strings.ToLower(text), "rewrite the") {
			return fmt.Errorf("%w: issue on seg %s", ErrCriticRewrote, issue.SegID)
		}
	}
	return nil
}

// RuleCritic produces diagnose-only feedback for a draft.
func RuleCritic(d Draft, report AudienceReport) CriticFeedback {
	var issues []CriticIssue
	for _, drop := range report.DropOffs {
		segID := "hook"
		if drop.Second >= DropOffSecondPayoff {
			segID = "body"
		}
		issues = append(issues, CriticIssue{
			SegID:    segID,
			Problem:  fmt.Sprintf("viewers leave around %ds", drop.Second),
			Evidence: drop.Reason,
		})
	}
	fb := CriticFeedback{Issues: issues}
	_ = fb.Validate()
	return fb
}

// RejectingCritic wraps feedback that intentionally contains rewrites (for tests).
func RejectingCritic() CriticFeedback {
	return CriticFeedback{Issues: []CriticIssue{{
		SegID: "hook", Problem: "opening weak", Evidence: "instead write a stronger hook here",
	}}}
}
