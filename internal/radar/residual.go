package radar

import (
	"math"
	"slices"
	"time"
)

// HotThresholdZ is how far past the category baseline a post has to land before
// the radar calls it hot.
//
// Two is roughly the top 2% of a normal tail. It is written down here, before
// any data exists, for the same reason the recompile scrap threshold is: a
// threshold picked after looking at the numbers is not a threshold. Lower than
// this and the radar reports noise as signal, which is worse than reporting
// nothing, because a topic list nobody trusts still costs an hour to read.
const HotThresholdZ = 2.0

// MinBaselineSamples is how many posts a category needs before the radar will
// quote a residual for it.
//
// Below this the dispersion estimate is itself noise, and dividing by a noisy
// dispersion produces confident-looking nonsense. Eight is a judgement call:
// it is roughly what five imported accounts produce in a month, which is the
// smallest watch list the feature was scoped for.
const MinBaselineSamples = 8

// MaturityTauHours is the time constant of the view accumulation curve.
//
// Short-form video collects the large majority of its views inside two days,
// so a post six hours old has not failed, it is unfinished. Without this
// correction the radar would rank every post by how long it has been up.
const MaturityTauHours = 48

// minMaturity floors the correction. A post minutes old has a maturity near
// zero, and dividing by it projects three views into an imaginary blockbuster.
const minMaturity = 0.05

// madToSigma converts a median absolute deviation into a standard-deviation
// equivalent for a normal distribution.
const madToSigma = 1.4826

// Sample is one post's metrics joined to the account that published it.
//
// It is the unit the baseline is fitted over, and it is flat rather than a
// pair of structs because the fit reads the account's follower count and the
// post's view count in the same expression.
type Sample struct {
	PostID    string
	AccountID string
	Platform  string
	Category  string
	Followers int64
	Owned     bool

	Title           string
	DurationSeconds int64
	PublishedAt     time.Time
	ObservedAt      time.Time

	Views    int64
	Likes    int64
	Comments int64
	Shares   int64
	Saves    int64

	CompletionRate      float64
	CommentSamples      int
	UnansweredQuestions int
}

// SaveRate is saves per view. Saving is the strongest public proxy for "I will
// come back to this", which is what a copyable format looks like from outside.
func (s Sample) SaveRate() float64 {
	if s.Views <= 0 {
		return 0
	}
	return float64(s.Saves) / float64(s.Views)
}

// AgeHours is how long the post had been up when it was observed.
func (s Sample) AgeHours() float64 {
	if s.PublishedAt.IsZero() || s.ObservedAt.IsZero() {
		return 0
	}
	return s.ObservedAt.Sub(s.PublishedAt).Hours()
}

// MaturedViews projects the observed view count forward to what the post looks
// like once it has finished accumulating.
func (s Sample) MaturedViews() float64 {
	return float64(s.Views) / maturity(s.AgeHours())
}

// maturity is the share of a post's eventual views that a post of this age is
// expected to have collected, in (0,1].
func maturity(ageHours float64) float64 {
	if ageHours <= 0 {
		return minMaturity
	}
	m := 1 - math.Exp(-ageHours/MaturityTauHours)
	return math.Max(m, minMaturity)
}

// Baseline is what a category's posts normally do.
//
// Views are fitted against follower count rather than averaged, because view
// count scales with audience size and an average over a category with mixed
// account sizes describes no account in it. Rates are averaged, because a save
// rate does not systematically depend on how many followers an account has —
// it depends on whether the post was worth saving.
type Baseline struct {
	Category string
	// Samples is how many posts the view fit used. Under MinBaselineSamples the
	// baseline reports itself insufficient instead of quoting a residual.
	Samples int

	// intercept and slope fit log(1+maturedViews) = intercept + slope·log(1+followers).
	//
	// The slope is fitted, not fixed at 1. Fixing it at 1 is the same as ranking
	// by views-per-follower, and views are sublinear in followers, so that
	// ranking calls every large account cold and every small one hot before it
	// has looked at a single post.
	intercept float64
	slope     float64
	viewScale float64

	saveRateCenter float64
	saveRateScale  float64

	completionSamples int
	completionCenter  float64
	completionScale   float64
}

// Sufficient reports whether the baseline rests on enough posts to quote.
func (b Baseline) Sufficient() bool { return b.Samples >= MinBaselineSamples }

// FitBaselines groups samples by category and fits one baseline per group.
//
// Categories are fitted separately and never pooled. A cooking channel's
// ordinary post outperforms a niche tech channel's best one, and a shared
// baseline would mark the whole of one category hot and the whole of the other
// cold, permanently.
func FitBaselines(samples []Sample) map[string]Baseline {
	byCategory := map[string][]Sample{}
	for _, s := range samples {
		byCategory[s.Category] = append(byCategory[s.Category], s)
	}

	out := make(map[string]Baseline, len(byCategory))
	for category, group := range byCategory {
		out[category] = fitCategory(category, group)
	}
	return out
}

func fitCategory(category string, group []Sample) Baseline {
	b := Baseline{Category: category, Samples: len(group)}

	xs := make([]float64, 0, len(group))
	ys := make([]float64, 0, len(group))
	saveRates := make([]float64, 0, len(group))
	completions := make([]float64, 0, len(group))
	for _, s := range group {
		xs = append(xs, math.Log1p(float64(s.Followers)))
		ys = append(ys, math.Log1p(s.MaturedViews()))
		saveRates = append(saveRates, s.SaveRate())
		// A completion rate of exactly zero means the platform did not tell us,
		// not that nobody watched to the end. Feeding those zeros into the mean
		// would drag the baseline towards zero in proportion to how many
		// accounts the user does not own.
		if s.CompletionRate > 0 {
			completions = append(completions, s.CompletionRate)
		}
	}

	b.intercept, b.slope = fitLine(xs, ys)
	residuals := make([]float64, len(ys))
	for i := range ys {
		residuals[i] = ys[i] - (b.intercept + b.slope*xs[i])
	}
	b.viewScale = dispersion(residuals)

	b.saveRateCenter, b.saveRateScale = center(saveRates)
	b.completionSamples = len(completions)
	b.completionCenter, b.completionScale = center(completions)
	return b
}

// fitLine is ordinary least squares for one predictor.
//
// A zero-variance predictor — every watched account in the category the same
// size — yields a flat line at the mean rather than an error. That is the
// honest reading: with no spread in account size there is no size effect to
// separate out, so the residual falls back to deviation from the category
// average.
func fitLine(xs, ys []float64) (intercept, slope float64) {
	n := float64(len(xs))
	if n == 0 {
		return 0, 0
	}
	var sumX, sumY float64
	for i := range xs {
		sumX += xs[i]
		sumY += ys[i]
	}
	meanX, meanY := sumX/n, sumY/n

	var cov, varX float64
	for i := range xs {
		dx := xs[i] - meanX
		cov += dx * (ys[i] - meanY)
		varX += dx * dx
	}
	if varX < 1e-12 {
		return meanY, 0
	}
	slope = cov / varX
	return meanY - slope*meanX, slope
}

// dispersion is a median absolute deviation, scaled to compare with a standard
// deviation.
//
// A standard deviation is the wrong tool here and the reason is the whole
// point of the module: the outlier we are hunting for is also the observation
// that inflates the standard deviation, so a single spectacular post raises the
// bar high enough to disqualify itself. A MAD barely moves for one extreme
// value, which is exactly the behaviour a hot-post detector needs.
func dispersion(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	med := median(values)
	deviations := make([]float64, len(values))
	for i, v := range values {
		deviations[i] = math.Abs(v - med)
	}
	if mad := median(deviations) * madToSigma; mad > 1e-9 {
		return mad
	}
	// Every value identical, or nearly so. Fall back to the standard deviation
	// so that a category with one slightly different post still ranks it,
	// rather than reporting a zero scale that would make every z infinite.
	return stddev(values)
}

// center returns the median and the dispersion of a set of rates.
func center(values []float64) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	return median(values), dispersion(values)
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func stddev(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	var acc float64
	for _, v := range values {
		acc += (v - mean) * (v - mean)
	}
	return math.Sqrt(acc / float64(len(values)-1))
}

// Residuals is how far one post landed from its category baseline, per metric.
//
// Every field is a z-score, never an absolute number, which is the point of the
// module: comparing raw view counts across accounts of different sizes is the
// mistake this replaces.
type Residuals struct {
	// ViewZ is the size- and age-corrected view residual.
	ViewZ float64 `json:"view_z"`
	// SaveRateZ is the save rate residual. It stands on its own rather than
	// being folded into ViewZ: a post that few people saw but many saved is a
	// format worth copying, and any weighted blend with views would bury it.
	SaveRateZ float64 `json:"save_rate_z"`
	// CompletionZ is the completion rate residual, available only for accounts
	// the user owns.
	CompletionZ float64 `json:"completion_z"`
	// Score is the largest of the three.
	Score float64 `json:"score"`
	// Hot is Score at or past HotThresholdZ.
	Hot bool `json:"hot"`
	// Insufficient means the category had fewer than MinBaselineSamples posts,
	// so the scores above are zero because nothing could be said, not because
	// the post was ordinary.
	Insufficient bool `json:"insufficient"`
}

// Residual scores one sample against its category baseline.
func Residual(s Sample, b Baseline) Residuals {
	if !b.Sufficient() {
		return Residuals{Insufficient: true}
	}

	r := Residuals{
		ViewZ:     z(math.Log1p(s.MaturedViews()), b.intercept+b.slope*math.Log1p(float64(s.Followers)), b.viewScale),
		SaveRateZ: z(s.SaveRate(), b.saveRateCenter, b.saveRateScale),
	}
	if s.CompletionRate > 0 && b.completionSamples >= MinBaselineSamples {
		r.CompletionZ = z(s.CompletionRate, b.completionCenter, b.completionScale)
	}

	// The maximum, not a weighted blend. One metric being extraordinary is the
	// signal; averaging it against two ordinary ones divides it by three and
	// loses precisely the case this exists for — an unremarkable view count
	// hiding an extraordinary save rate.
	r.Score = max(r.ViewZ, r.SaveRateZ, r.CompletionZ)
	r.Hot = r.Score >= HotThresholdZ
	return r
}

// z is a standard score with a guarded scale. A zero scale means every sample
// in the category was identical, in which case nothing deviates and zero is the
// right answer — not an infinity.
func z(value, center, scale float64) float64 {
	if scale <= 1e-12 {
		return 0
	}
	return (value - center) / scale
}
