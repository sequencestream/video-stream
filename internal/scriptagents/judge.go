package scriptagents

import (
	"context"
	"strings"

	"github.com/sequencestream/video-stream/internal/model"
)

// JudgeScore ranks a draft after audience simulation.
type JudgeScore struct {
	DraftID   string  `json:"draft_id"`
	Score     float64 `json:"score"`
	Direction Direction `json:"direction"`
}

// Judge picks the leading draft and identifies an eliminated draft for hybridisation.
type Judge interface {
	Rank(ctx context.Context, drafts []Draft, reports map[string]AudienceReport) ([]JudgeScore, error)
}

// RuleJudge scores drafts by fewer predicted drop-offs and direction diversity bonus.
type RuleJudge struct{}

// Rank implements Judge.
func (RuleJudge) Rank(_ context.Context, drafts []Draft, reports map[string]AudienceReport) ([]JudgeScore, error) {
	scores := make([]JudgeScore, 0, len(drafts))
	for _, d := range drafts {
		report := reports[d.ID]
		// Lower drop-off count at hook moment = higher score baseline.
		score := 1.0
		for _, drop := range report.DropOffs {
			if drop.Second == DropOffSecondHook {
				score -= 0.05
			}
		}
		switch d.Direction {
		case DirectionContrarian:
			score += 0.02
		case DirectionQuestion:
			score += 0.01
		}
		scores = append(scores, JudgeScore{DraftID: d.ID, Score: score, Direction: d.Direction})
	}
	// Stable sort by score desc, then draft id.
	for i := 0; i < len(scores); i++ {
		for j := i + 1; j < len(scores); j++ {
			if scores[j].Score > scores[i].Score ||
				(scores[j].Score == scores[i].Score && scores[j].DraftID < scores[i].DraftID) {
				scores[i], scores[j] = scores[j], scores[i]
			}
		}
	}
	return scores, nil
}

// HybridiseHook replaces the winner's hook seg text with the eliminated draft's hook.
// Feature-level hybrid — take the hook paragraph, not an averaged blend.
func HybridiseHook(winner, eliminated Draft) Draft {
	if len(winner.Segs) == 0 || len(eliminated.Segs) == 0 {
		return winner
	}
	out := winner
	out.Segs = append([]model.Seg{}, winner.Segs...)
	for i := range out.Segs {
		if out.Segs[i].SegID == "hook" && eliminated.HookText != "" {
			out.Segs[i].Text = eliminated.HookText
			break
		}
	}
	return out
}

// draftByID finds a draft in a slice.
func draftByID(drafts []Draft, id string) (Draft, bool) {
	for _, d := range drafts {
		if d.ID == id {
			return d, true
		}
	}
	return Draft{}, false
}

// styleAnchorReject reports whether text smells like generic AI phrasing vs user quotes.
func styleAnchorReject(text string, userQuotes []string) bool {
	aiSmell := []string{"in today's fast-paced", "game-changer", "delve", "landscape"}
	lower := strings.ToLower(text)
	for _, smell := range aiSmell {
		if strings.Contains(lower, smell) {
			for _, q := range userQuotes {
				if strings.Contains(lower, strings.ToLower(q)) {
					return false
				}
			}
			return true
		}
	}
	return false
}
