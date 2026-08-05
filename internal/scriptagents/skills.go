package scriptagents

import (
	"strings"
	"unicode/utf8"

	"github.com/sequencestream/video-stream/internal/model"
)

// SkillResults are deterministic checks — not LLM agents.
type SkillResults struct {
	FactsOK    bool    `json:"facts_ok"`
	PolicyOK   bool    `json:"policy_ok"`
	BreathOK   bool    `json:"breath_ok"`
	CostMicros int64   `json:"cost_micros"`
	Notes      []string `json:"notes,omitempty"`
}

// RunSkills applies fact check, policy, breath points, and cost estimate.
func RunSkills(d Draft, cfg TerminationConfig) SkillResults {
	res := SkillResults{FactsOK: true, PolicyOK: true, BreathOK: true}
	res.Notes = append(res.Notes, checkFacts(d)...)
	res.Notes = append(res.Notes, checkPolicy(d)...)
	res.BreathOK = checkBreathPoints(d)
	if !res.BreathOK {
		res.Notes = append(res.Notes, "subtitle breath points missing on long segs")
	}
	res.CostMicros = estimateCostMicros(d, cfg.CostPer1KTokensMicros)
	if len(res.Notes) > 0 {
		for _, n := range res.Notes {
			if strings.HasPrefix(n, "policy:") {
				res.PolicyOK = false
			}
			if strings.HasPrefix(n, "fact:") {
				res.FactsOK = false
			}
		}
	}
	return res
}

func checkFacts(d Draft) []string {
	var notes []string
	for _, s := range d.Segs {
		if strings.Contains(strings.ToLower(s.Text), "guaranteed cure") {
			notes = append(notes, "fact: unsubstantiated medical claim in seg "+s.SegID)
		}
	}
	return notes
}

func checkPolicy(d Draft) []string {
	var notes []string
	banned := []string{"buy followers", "hack the algorithm"}
	for _, s := range d.Segs {
		lower := strings.ToLower(s.Text)
		for _, b := range banned {
			if strings.Contains(lower, b) {
				notes = append(notes, "policy: banned phrase in seg "+s.SegID)
			}
		}
	}
	return notes
}

func checkBreathPoints(d Draft) bool {
	for _, s := range d.Segs {
		if utf8.RuneCountInString(s.Text) > 80 && len(s.SubtitleBreaks) == 0 {
			return false
		}
	}
	return true
}

func estimateCostMicros(d Draft, rate int64) int64 {
	if rate <= 0 {
		rate = 500
	}
	tokens := d.TokensUsed
	if tokens == 0 {
		for _, s := range d.Segs {
			tokens += int64(utf8.RuneCountInString(s.Text))
		}
	}
	return tokens * rate / 1000
}

// ApplyBreathPoints inserts subtitle breaks on long segs.
func ApplyBreathPoints(d Draft) Draft {
	out := d
	out.Segs = append([]model.Seg{}, d.Segs...)
	for i := range out.Segs {
		text := out.Segs[i].Text
		if utf8.RuneCountInString(text) <= 80 {
			continue
		}
		mid := utf8.RuneCountInString(text) / 2
		out.Segs[i].SubtitleBreaks = []int{mid}
	}
	return out
}

// HardConstraintsPass reports whether all deterministic skills passed.
func HardConstraintsPass(r SkillResults) bool {
	return r.FactsOK && r.PolicyOK && r.BreathOK
}
