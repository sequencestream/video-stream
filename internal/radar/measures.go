package radar

import (
	"math"
	"slices"
	"strings"
	"unicode"
)

// mismatchSpanDecades is the reach-to-audience ratio, in powers of ten, that
// counts as a total identity mismatch. A post reaching a thousand times the
// account's follower count was distributed by the platform, not by the
// audience, and that is the ceiling of the scale.
const mismatchSpanDecades = 3

// minCostProxyMinutes floors the production cost proxy. A post with no recorded
// duration would otherwise divide by zero and rank first on every arbitrage
// list purely for being under-described.
const minCostProxyMinutes = 0.25

// MeasureIdentityMismatch scores how badly a post outran the account that
// published it, in [0,1].
//
// This is the replicability measure, and it is the one that decides whether a
// hot post is worth reading at all. A channel with two million followers
// clearing two million views has demonstrated that it has two million
// followers. A channel with two thousand followers doing the same has
// demonstrated something about the post, and that something is transferable.
//
// It is deliberately not redundant with ViewZ. The residual already corrects
// for account size, so it answers "did this beat expectation". This answers
// "was the distribution algorithmic rather than owned", which is a different
// question with a different action attached: only the second one is copyable.
// Both have to hold, which is why the two are multiplied rather than added.
func MeasureIdentityMismatch(s Sample, r Residuals) float64 {
	if s.Followers <= 0 || r.Insufficient {
		return 0
	}
	ratio := s.MaturedViews() / float64(s.Followers)
	if ratio <= 1 {
		return 0
	}

	spread := math.Log10(ratio) / mismatchSpanDecades
	beat := r.ViewZ / HotThresholdZ
	return clamp01(spread) * clamp01(beat)
}

// Acceleration is the second derivative of a post's engagement rates.
//
// The first derivative says a post is still growing, which every live post is.
// The second says whether it is still gaining speed, and that is the part that
// decides whether entering the topic now is early or late. A decelerating post
// with a spectacular residual is a report on something that already happened.
type Acceleration struct {
	// SaveRate is the change in the save rate's rate of change, per hour
	// squared.
	SaveRate float64 `json:"save_rate"`
	// Completion is the same for the completion rate, and is zero for accounts
	// the user does not own.
	Completion float64 `json:"completion"`
	// Points is how many readings the series had.
	Points int `json:"points"`
	// Measurable is false below three readings. A second derivative needs three
	// points, and reporting zero for a two-point series would be
	// indistinguishable from a genuinely flat one.
	Measurable bool `json:"measurable"`
}

// MeasureAcceleration differentiates a post's rate series twice.
//
// series must be the readings of one post in observation order, oldest first.
// Only the last three are used: acceleration is a statement about now, and
// folding in a reading from three weeks ago answers a question nobody asked.
func MeasureAcceleration(series []Sample) Acceleration {
	a := Acceleration{Points: len(series)}
	if len(series) < 3 {
		return a
	}

	last := series[len(series)-3:]
	t0, t1, t2 := last[0].ObservedAt, last[1].ObservedAt, last[2].ObservedAt
	firstSpan := t1.Sub(t0).Hours()
	secondSpan := t2.Sub(t1).Hours()
	// Readings taken at the same instant are a duplicated row, not a series.
	if firstSpan <= 0 || secondSpan <= 0 {
		return a
	}

	a.Measurable = true
	a.SaveRate = secondDerivative(
		last[0].SaveRate(), last[1].SaveRate(), last[2].SaveRate(), firstSpan, secondSpan)
	if last[0].CompletionRate > 0 && last[1].CompletionRate > 0 && last[2].CompletionRate > 0 {
		a.Completion = secondDerivative(
			last[0].CompletionRate, last[1].CompletionRate, last[2].CompletionRate, firstSpan, secondSpan)
	}
	return a
}

// secondDerivative is the difference of two difference quotients over unevenly
// spaced points, divided by the mean of the two spans.
//
// Dividing by the mean span rather than by either one keeps the result
// comparable between a post polled hourly and a post polled daily, which
// matters because the polling interval is a configuration value and the measure
// must not change meaning when a user edits it.
func secondDerivative(r0, r1, r2, firstSpan, secondSpan float64) float64 {
	first := (r1 - r0) / firstSpan
	second := (r2 - r1) / secondSpan
	return (second - first) / ((firstSpan + secondSpan) / 2)
}

// Arbitrage is the cost-arbitrage window: how much unexpected performance a
// post bought per unit of production effort, and whether the window is still
// open.
//
// The cost side is a crude proxy — finished duration — and it is worth being
// explicit that it is wrong in a knowable direction: a three-minute static
// talking head and a three-minute multi-shot edit cost wildly different
// amounts and score identically here. It is used anyway because duration is the
// only cost signal a public metrics page carries, and a bad cost estimate still
// separates a sixty-second clip from a twelve-minute production.
type Arbitrage struct {
	// Score is residual per minute of finished video. Zero when the post did
	// not beat its baseline: there is no arbitrage in an ordinary post however
	// cheap it was.
	Score float64 `json:"score"`
	// CostProxyMinutes is the denominator, exposed so the crudeness is visible
	// rather than buried.
	CostProxyMinutes float64 `json:"cost_proxy_minutes"`
	// Open means the opportunity is still live: the post beat its baseline, it
	// has not stopped gaining speed, and it is inside the observation window.
	Open bool `json:"open"`
	// AgeHours is how long the post has been up.
	AgeHours float64 `json:"age_hours"`
}

// MeasureArbitrage scores the cost-arbitrage window for one post.
func MeasureArbitrage(s Sample, r Residuals, a Acceleration) Arbitrage {
	arb := Arbitrage{
		CostProxyMinutes: math.Max(float64(s.DurationSeconds)/60, minCostProxyMinutes),
		AgeHours:         s.AgeHours(),
	}
	if r.Insufficient || r.Score <= 0 {
		return arb
	}

	arb.Score = r.Score / arb.CostProxyMinutes
	// An unmeasurable acceleration leaves the window open. Two readings is not
	// evidence of decay, and closing the window on missing data would hide
	// every post the radar has only just started watching — which is every post
	// that is actually early.
	stillClimbing := !a.Measurable || a.SaveRate >= 0
	arb.Open = stillClimbing && arb.AgeHours <= ObservationWindowDays*24
	return arb
}

// QuestionDensity is the share of sampled comments that asked something the
// author never answered.
//
// Unanswered questions under a post that is already working are the closest
// thing to a free topic list this product will ever get: someone has stated a
// gap, in their own words, in a place where the demand for the answer is
// already demonstrated.
//
// The radar counts them and stops there. Replying is off the table — automated
// comment replies are the inauthentic-behaviour rule on every platform, and
// the account at risk is the user's.
type QuestionDensity struct {
	Unanswered int `json:"unanswered"`
	Sampled    int `json:"sampled"`
	// Density is Unanswered over Sampled, in [0,1]. Zero sampled comments gives
	// zero, which Sampled disambiguates from a post whose questions were all
	// answered.
	Density float64 `json:"density"`
}

// MeasureQuestionDensity reduces a comment sample to a density.
func MeasureQuestionDensity(comments []Comment) QuestionDensity {
	q := QuestionDensity{Sampled: len(comments)}
	for _, c := range comments {
		if !c.AuthorReplied && IsQuestion(c.Text) {
			q.Unanswered++
		}
	}
	if q.Sampled > 0 {
		q.Density = float64(q.Unanswered) / float64(q.Sampled)
	}
	return q
}

// density rebuilds a QuestionDensity from persisted counts.
func density(unanswered, sampled int) QuestionDensity {
	q := QuestionDensity{Unanswered: unanswered, Sampled: sampled}
	if sampled > 0 {
		q.Density = float64(unanswered) / float64(sampled)
	}
	return q
}

// cjkQuestionMarkers are interrogative fragments that carry the question
// without a question mark, which is how most comments are actually written.
//
// The last few — 求教程, 求链接, 蹲 — are requests rather than grammatical
// questions. They are included because the measure exists to find stated gaps,
// and "求教程" is a stated gap in the most actionable form there is.
var cjkQuestionMarkers = []string{
	"怎么", "咋", "如何", "为什么", "为啥", "哪里", "哪儿", "哪个", "哪家",
	"什么", "多少", "几点", "能不能", "是不是", "有没有", "可不可以", "行不行",
	"请问", "求问", "求教程", "求链接", "求同款", "蹲一个",
}

// asciiQuestionMarkers are matched against the first word only.
//
// English questions front their interrogative, so the first word carries almost
// all of the signal. Matching anywhere in the sentence would count "this is
// great" and "however you like" as questions, and those are common enough in a
// comment section to swamp the measure.
var asciiQuestionMarkers = []string{
	"how", "what", "why", "where", "which", "who", "when", "whose",
	"can", "could", "does", "do", "did", "is", "are", "will", "would",
	"should", "any", "anyone", "anybody",
}

// IsQuestion reports whether a comment is asking something.
//
// It is a keyword test, not a parser, and it is wrong in both directions: it
// counts "没什么好看的" as a question and misses one phrased as a statement. That
// is acceptable because the output is a density compared against other posts,
// and a consistent error rate cancels out of the comparison. A parser would be
// a large dependency bought to make a ranking marginally less noisy.
func IsQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "?？") {
		return true
	}

	for _, marker := range cjkQuestionMarkers {
		if strings.Contains(trimmed, marker) {
			return true
		}
	}

	words := strings.FieldsFunc(strings.ToLower(trimmed), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return false
	}
	return slices.Contains(asciiQuestionMarkers, words[0])
}

func clamp01(v float64) float64 {
	return math.Min(math.Max(v, 0), 1)
}
