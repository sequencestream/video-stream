package radar_test

import (
	"math"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/radar"
)

// The fixture below is a log-linear category: view count rises with follower
// count along a fixed line, with a small spread around it. It is generated
// rather than hand-written so that the two acceptance cases differ from the
// ordinary posts in exactly one respect each.
const (
	fixtureIntercept = 2.0
	fixtureSlope     = 0.8
	fixtureAgeHours  = 240
	fixtureSaveRate  = 0.05
)

var fixturePublished = time.UnixMilli(1_700_000_000_000).UTC()

// ordinaryPost is a post that behaved exactly as an account of its size in this
// category is expected to, give or take logJitter.
func ordinaryPost(id string, followers int64, logJitter, saveRate float64) radar.Sample {
	matured := math.Exp(fixtureIntercept+fixtureSlope*math.Log1p(float64(followers))+logJitter) - 1
	views := int64(matured * (1 - math.Exp(-fixtureAgeHours/float64(radar.MaturityTauHours))))
	return radar.Sample{
		PostID:          id,
		AccountID:       "acc-" + id,
		Platform:        "douyin",
		Category:        "cooking",
		Followers:       followers,
		Title:           id,
		DurationSeconds: 120,
		PublishedAt:     fixturePublished,
		ObservedAt:      fixturePublished.Add(fixtureAgeHours * time.Hour),
		Views:           views,
		Saves:           int64(float64(views) * saveRate),
	}
}

func cookingCategory() []radar.Sample {
	followers := []int64{2_000, 5_000, 12_000, 30_000, 80_000, 150_000, 400_000, 900_000, 1_500_000, 2_000_000}
	jitter := []float64{0.12, -0.09, 0.05, -0.14, 0.08, -0.06, 0.11, -0.10, 0.04, -0.07}
	saveJitter := []float64{0.004, -0.003, 0.002, -0.005, 0.003, -0.002, 0.004, -0.004, 0.001, -0.003}

	out := make([]radar.Sample, 0, len(followers))
	for i, f := range followers {
		out = append(out, ordinaryPost(string(rune('a'+i)), f, jitter[i], fixtureSaveRate+saveJitter[i]))
	}
	return out
}

func baselineFor(t *testing.T, samples []radar.Sample) radar.Baseline {
	t.Helper()
	b, ok := radar.FitBaselines(samples)["cooking"]
	if !ok {
		t.Fatal("FitBaselines produced no cooking baseline")
	}
	if !b.Sufficient() {
		t.Fatalf("baseline is insufficient with %d samples", b.Samples)
	}
	return b
}

// This is the case the whole module exists to get right. The large account's
// post has by far the highest absolute view count in the category, and it is
// still not a hot post: it is exactly what two million followers produce.
// Anything that ranks by views, or by a fixed views-per-follower ratio, fails
// here.
func TestARoutineViewCountFromALargeAccountIsNotHot(t *testing.T) {
	probe := ordinaryPost("large", 2_000_000, 0, fixtureSaveRate)
	samples := append(cookingCategory(), probe)

	for _, s := range samples {
		if s.PostID != "large" && s.Views > probe.Views {
			t.Fatalf("fixture is not testing what it claims: %s has more views (%d) than the large account (%d)",
				s.PostID, s.Views, probe.Views)
		}
	}

	got := radar.Residual(probe, baselineFor(t, samples))
	if got.Hot {
		t.Fatalf("a routine post from a large account was called hot: %+v", got)
	}
	if math.Abs(got.ViewZ) >= radar.HotThresholdZ {
		t.Fatalf("view residual %.2f should be near zero for a post on the baseline", got.ViewZ)
	}
}

// The complement: a post nobody would notice by view count, published by an
// account nobody would notice either, that a fifth of its viewers saved. The
// save rate stands on its own precisely so this case survives — folding it into
// a weighted score with the ordinary view count would divide it away.
func TestAnAnomalousSaveRateFromASmallAccountIsHot(t *testing.T) {
	probe := ordinaryPost("small", 2_000, 0, 0.30)
	samples := append(cookingCategory(), probe)

	got := radar.Residual(probe, baselineFor(t, samples))
	if !got.Hot {
		t.Fatalf("an anomalous save rate was not called hot: %+v", got)
	}
	if got.SaveRateZ < radar.HotThresholdZ {
		t.Fatalf("save rate residual %.2f should be past the threshold", got.SaveRateZ)
	}
	// The view count is ordinary, and the report must say so rather than
	// smearing the save-rate anomaly across every metric.
	if math.Abs(got.ViewZ) >= radar.HotThresholdZ {
		t.Fatalf("view residual %.2f should be ordinary for this post", got.ViewZ)
	}
}

// One spectacular post inflates a standard deviation enough to disqualify
// itself. A median absolute deviation barely moves, which is why the dispersion
// is a MAD.
func TestASingleExtremeOutlierDoesNotHideItselfInTheDispersion(t *testing.T) {
	probe := ordinaryPost("viral", 12_000, 3.0, fixtureSaveRate)
	samples := append(cookingCategory(), probe)

	got := radar.Residual(probe, baselineFor(t, samples))
	if !got.Hot {
		t.Fatalf("an outlier three log units above the line was not hot: %+v", got)
	}
}

// Under MinBaselineSamples the dispersion is itself noise, and dividing by it
// produces confident nonsense. Saying nothing has to be a distinct answer from
// saying "ordinary".
func TestATooSmallCategoryQuotesNoResidual(t *testing.T) {
	samples := cookingCategory()[:radar.MinBaselineSamples-1]
	b := radar.FitBaselines(samples)["cooking"]
	if b.Sufficient() {
		t.Fatalf("%d samples should not be sufficient", b.Samples)
	}

	got := radar.Residual(samples[0], b)
	if !got.Insufficient {
		t.Fatalf("got %+v, want Insufficient", got)
	}
	if got.Hot || got.Score != 0 {
		t.Fatalf("an insufficient baseline must not produce a score: %+v", got)
	}
}

// Without the maturity correction the radar would rank posts by how long they
// have been up, and every genuinely early signal would look like a failure.
func TestAYoungPostIsNotPenalisedForHavingHadLessTime(t *testing.T) {
	mature := ordinaryPost("mature", 30_000, 0, fixtureSaveRate)

	// The same post six hours in, holding the share of its eventual views that
	// the maturity curve expects by then.
	young := mature
	young.PostID = "young"
	young.ObservedAt = mature.PublishedAt.Add(6 * time.Hour)
	young.Views = int64(mature.MaturedViews() * (1 - math.Exp(-6.0/float64(radar.MaturityTauHours))))
	young.Saves = int64(float64(young.Views) * fixtureSaveRate)

	b := baselineFor(t, append(cookingCategory(), mature))
	matureZ := radar.Residual(mature, b).ViewZ
	youngZ := radar.Residual(young, b).ViewZ

	if math.Abs(matureZ-youngZ) > 0.5 {
		t.Fatalf("age changed the residual: mature %.2f, young %.2f", matureZ, youngZ)
	}
	if young.Views >= mature.Views {
		t.Fatal("fixture is wrong: the young post should have fewer raw views")
	}
}

// With no spread in account size there is no size effect to separate out. The
// honest fallback is deviation from the category average, not a division by a
// zero variance.
func TestACategoryOfIdenticallySizedAccountsStillRanks(t *testing.T) {
	var samples []radar.Sample
	jitter := []float64{0.12, -0.09, 0.05, -0.14, 0.08, -0.06, 0.11, -0.10, 0.04, -0.07}
	for i, j := range jitter {
		samples = append(samples, ordinaryPost(string(rune('a'+i)), 50_000, j, fixtureSaveRate))
	}
	probe := ordinaryPost("standout", 50_000, 2.5, fixtureSaveRate)
	samples = append(samples, probe)

	got := radar.Residual(probe, baselineFor(t, samples))
	if !got.Hot {
		t.Fatalf("a standout in a flat category was not hot: %+v", got)
	}
}

// Completion rate only exists for accounts the user owns, so most rows carry a
// zero. Averaging those zeros in would drag the baseline down in proportion to
// how many accounts are not owned, and every owned post would look superb.
func TestUnavailableCompletionRatesAreNotTreatedAsZeroCompletion(t *testing.T) {
	samples := cookingCategory()
	for i := range samples {
		if i%2 == 0 {
			samples[i].Owned = true
			samples[i].CompletionRate = 0.5
		}
	}
	probe := ordinaryPost("owned", 30_000, 0, fixtureSaveRate)
	probe.Owned = true
	probe.CompletionRate = 0.5
	samples = append(samples, probe)

	got := radar.Residual(probe, baselineFor(t, samples))
	if got.CompletionZ != 0 {
		t.Fatalf("completion residual %.2f: with only 6 owned samples the baseline is insufficient and must be skipped", got.CompletionZ)
	}
	if got.Hot {
		t.Fatalf("an average post was called hot through the completion channel: %+v", got)
	}
}
