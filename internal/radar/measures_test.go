package radar_test

import (
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/radar"
)

// --- identity mismatch ---------------------------------------------------

// Two posts with the same view count and the same residual, from accounts three
// orders of magnitude apart. Only the small one is copyable, and the measure
// has to say so — this is the difference between a topic list and a list of
// things famous people did.
func TestIdentityMismatchSeparatesASmallAccountFromALargeOneAtEqualReach(t *testing.T) {
	beat := radar.Residuals{ViewZ: 3.0, Score: 3.0, Hot: true}

	small := ordinaryPost("small", 2_000, 0, fixtureSaveRate)
	small.Views = 500_000
	large := ordinaryPost("large", 2_000_000, 0, fixtureSaveRate)
	large.Views = 500_000

	smallScore := radar.MeasureIdentityMismatch(small, beat)
	largeScore := radar.MeasureIdentityMismatch(large, beat)

	if smallScore <= largeScore {
		t.Fatalf("small %.3f should outscore large %.3f at equal reach", smallScore, largeScore)
	}
	if smallScore < 0.6 {
		t.Fatalf("a post reaching 250x its audience scored only %.3f", smallScore)
	}
	if largeScore != 0 {
		t.Fatalf("a post reaching a quarter of its audience scored %.3f, want 0", largeScore)
	}
}

// Reach alone is not the measure. An account whose every post reaches ten times
// its follower count has an algorithmic distribution, not a discovery, and the
// residual is what tells the two apart — so the two are multiplied.
func TestIdentityMismatchIsZeroWhenThePostDidNotBeatItsBaseline(t *testing.T) {
	s := ordinaryPost("small", 2_000, 0, fixtureSaveRate)
	s.Views = 500_000

	got := radar.MeasureIdentityMismatch(s, radar.Residuals{ViewZ: 0})
	if got != 0 {
		t.Fatalf("got %.3f, want 0 for a post that only met expectations", got)
	}
}

func TestIdentityMismatchIsZeroWhenTheFollowerCountIsUnknown(t *testing.T) {
	s := ordinaryPost("unknown", 0, 0, fixtureSaveRate)
	s.Followers = 0
	s.Views = 500_000

	if got := radar.MeasureIdentityMismatch(s, radar.Residuals{ViewZ: 5}); got != 0 {
		t.Fatalf("got %.3f, want 0 when the denominator is unknown", got)
	}
}

// --- acceleration --------------------------------------------------------

func saveSeries(rates []float64, spacing time.Duration) []radar.Sample {
	out := make([]radar.Sample, 0, len(rates))
	for i, rate := range rates {
		out = append(out, radar.Sample{
			PostID:      "p1",
			PublishedAt: fixturePublished,
			ObservedAt:  fixturePublished.Add(time.Duration(i) * spacing),
			Views:       10_000,
			Saves:       int64(10_000 * rate),
		})
	}
	return out
}

// A post whose save rate is gaining speed is one to enter now. The first
// derivative cannot say this: both of these series are rising.
func TestAccelerationIsPositiveWhenTheSaveRateIsStillGainingSpeed(t *testing.T) {
	got := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.04, 0.09}, time.Hour))
	if !got.Measurable {
		t.Fatalf("three readings should be measurable: %+v", got)
	}
	if got.SaveRate <= 0 {
		t.Fatalf("got %.6f, want a positive second derivative", got.SaveRate)
	}
}

func TestAccelerationIsNegativeWhenAStillRisingSaveRateIsSlowingDown(t *testing.T) {
	got := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.07, 0.08}, time.Hour))
	if got.SaveRate >= 0 {
		t.Fatalf("got %.6f, want a negative second derivative for a decelerating rise", got.SaveRate)
	}
}

// Reporting zero for a two-point series would be indistinguishable from a
// genuinely flat one, and the arbitrage window reads a flat series as still
// open.
func TestAccelerationIsUnmeasurableBelowThreeReadings(t *testing.T) {
	got := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.05}, time.Hour))
	if got.Measurable {
		t.Fatalf("two readings should not be measurable: %+v", got)
	}
	if got.Points != 2 {
		t.Fatalf("got %d points, want 2", got.Points)
	}
}

// The polling interval is a configuration value. If the measure changed
// magnitude when a user widened it, the numbers would stop being comparable
// across installations.
func TestAccelerationDoesNotChangeSignWithThePollingInterval(t *testing.T) {
	hourly := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.04, 0.09}, time.Hour))
	daily := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.04, 0.09}, 24*time.Hour))

	if hourly.SaveRate <= 0 || daily.SaveRate <= 0 {
		t.Fatalf("both should be positive: hourly %.6f, daily %.6f", hourly.SaveRate, daily.SaveRate)
	}
}

// Two readings written at the same instant are a duplicated row, not a series,
// and dividing by their zero span would produce an infinity.
func TestAccelerationRejectsReadingsTakenAtTheSameInstant(t *testing.T) {
	series := saveSeries([]float64{0.02, 0.04, 0.09}, time.Hour)
	series[2].ObservedAt = series[1].ObservedAt

	if got := radar.MeasureAcceleration(series); got.Measurable {
		t.Fatalf("a zero span should not be measurable: %+v", got)
	}
}

// --- arbitrage window ----------------------------------------------------

// The same unexpected performance bought with a fifth of the production effort
// is a better opportunity, and that ratio is the whole measure.
func TestArbitrageRanksACheaperPostHigherAtEqualResidual(t *testing.T) {
	beat := radar.Residuals{ViewZ: 3, Score: 3, Hot: true}
	rising := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.04, 0.09}, time.Hour))

	cheap := ordinaryPost("cheap", 30_000, 0, fixtureSaveRate)
	cheap.DurationSeconds = 60
	expensive := ordinaryPost("expensive", 30_000, 0, fixtureSaveRate)
	expensive.DurationSeconds = 600

	cheapArb := radar.MeasureArbitrage(cheap, beat, rising)
	expensiveArb := radar.MeasureArbitrage(expensive, beat, rising)

	if cheapArb.Score <= expensiveArb.Score {
		t.Fatalf("cheap %.3f should outscore expensive %.3f", cheapArb.Score, expensiveArb.Score)
	}
	if !cheapArb.Open {
		t.Fatalf("a rising post inside the window should be open: %+v", cheapArb)
	}
}

// A decelerating post with a spectacular residual is a report on something that
// already happened.
func TestArbitrageClosesTheWindowOnADeceleratingPost(t *testing.T) {
	falling := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.07, 0.08}, time.Hour))
	got := radar.MeasureArbitrage(ordinaryPost("p", 30_000, 0, fixtureSaveRate),
		radar.Residuals{ViewZ: 3, Score: 3, Hot: true}, falling)

	if got.Open {
		t.Fatalf("a decelerating post should have a closed window: %+v", got)
	}
	// The score still stands: the opportunity existed, it is just no longer
	// early. Zeroing it would erase the evidence that the format works.
	if got.Score <= 0 {
		t.Fatalf("got score %.3f, want the ratio to survive the window closing", got.Score)
	}
}

// Two readings is not evidence of decay. Closing on missing data would hide
// every post the radar has only just started watching, which is every post that
// is actually early.
func TestArbitrageLeavesTheWindowOpenWhenAccelerationIsUnmeasurable(t *testing.T) {
	unknown := radar.MeasureAcceleration(saveSeries([]float64{0.02, 0.05}, time.Hour))
	got := radar.MeasureArbitrage(ordinaryPost("p", 30_000, 0, fixtureSaveRate),
		radar.Residuals{ViewZ: 3, Score: 3, Hot: true}, unknown)

	if !got.Open {
		t.Fatalf("an unmeasurable acceleration should leave the window open: %+v", got)
	}
}

// There is no arbitrage in an ordinary post, however cheap it was.
func TestArbitrageScoresNothingForAPostThatMetExpectations(t *testing.T) {
	got := radar.MeasureArbitrage(ordinaryPost("p", 30_000, 0, fixtureSaveRate),
		radar.Residuals{Score: 0}, radar.Acceleration{})

	if got.Score != 0 || got.Open {
		t.Fatalf("got %+v, want a zero score and a closed window", got)
	}
}

// A post with no recorded duration would divide by zero and top every list for
// being under-described.
func TestArbitrageFloorsTheCostProxyForAPostOfUnknownLength(t *testing.T) {
	s := ordinaryPost("p", 30_000, 0, fixtureSaveRate)
	s.DurationSeconds = 0

	got := radar.MeasureArbitrage(s, radar.Residuals{ViewZ: 3, Score: 3, Hot: true}, radar.Acceleration{})
	if got.CostProxyMinutes <= 0 {
		t.Fatalf("cost proxy %.3f must be positive", got.CostProxyMinutes)
	}
}

// --- unanswered question density -----------------------------------------

func TestQuestionDensityCountsOnlyUnansweredQuestions(t *testing.T) {
	got := radar.MeasureQuestionDensity([]radar.Comment{
		{Text: "这个用的什么牌子的锅"},                       // question, unanswered
		{Text: "求教程"},                              // stated gap, unanswered
		{Text: "怎么做才不粘锅", AuthorReplied: true},      // question, answered
		{Text: "太好看了"},                             // not a question
		{Text: "How long do you preheat it?"},      // question, unanswered
		{Text: "This is great, however you do it"}, // not a question
	})

	if got.Sampled != 6 {
		t.Fatalf("got %d sampled, want 6", got.Sampled)
	}
	if got.Unanswered != 3 {
		t.Fatalf("got %d unanswered, want 3", got.Unanswered)
	}
	if got.Density != 0.5 {
		t.Fatalf("got density %.3f, want 0.5", got.Density)
	}
}

// Nine unanswered questions out of ten comments and out of ten thousand are
// opposite findings, so the sample size travels with the count.
func TestQuestionDensityReportsTheSampleSizeAlongsideTheCount(t *testing.T) {
	got := radar.MeasureQuestionDensity(nil)
	if got.Sampled != 0 || got.Density != 0 {
		t.Fatalf("got %+v, want an empty sample rather than a zero density", got)
	}
}

func TestIsQuestionRecognisesTheFormsCommentsActuallyUse(t *testing.T) {
	for _, run := range []struct {
		text string
		want bool
	}{
		{"这是在哪买的?", true},
		{"这是在哪买的？", true},
		{"用的什么滤镜", true},
		{"求链接", true},
		{"能不能出个教程", true},
		{"How did you cut that so evenly", true},
		{"Any tips for beginners", true},
		{"Is this a real recipe", true},
		{"太厉害了", false},
		{"没什么好说的就是牛", true}, // known false positive: 什么 without a question
		{"However you slice it this is good", false},
		{"", false},
		{"   ", false},
	} {
		if got := radar.IsQuestion(run.text); got != run.want {
			t.Errorf("IsQuestion(%q) = %v, want %v", run.text, got, run.want)
		}
	}
}
