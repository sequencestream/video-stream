package model_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
)

func TestNewDurationBudgetStaysInsideTheEightPercentBand(t *testing.T) {
	for _, targetMS := range []int64{100, 999, 1000, 2500, 60_000} {
		b := model.NewDurationBudget(targetMS)
		if err := b.Validate(); err != nil {
			t.Fatalf("target %dms produced an invalid budget %+v: %v", targetMS, b, err)
		}

		// Rounding must go inward: the real tolerance may never exceed 8%.
		span, sum := b.MaxMS-b.MinMS, b.MinMS+b.MaxMS
		if 100*span > model.MaxTolerancePercent*sum {
			t.Fatalf("target %dms produced %+v, wider than ±%d%%", targetMS, b, model.MaxTolerancePercent)
		}
		if !b.Contains(targetMS) {
			t.Fatalf("budget %+v does not contain its own target %dms", b, targetMS)
		}
	}
}

// A fixed duration is the failure mode this whole type exists to prevent: with
// a point budget a cached artifact would have to match to the millisecond, so
// nothing would ever be reused.
func TestDurationBudgetRejectsAFixedValue(t *testing.T) {
	err := model.DurationBudget{MinMS: 2000, MaxMS: 2000}.Validate()
	if !errors.Is(err, model.ErrFixedDurationBudget) {
		t.Fatalf("got %v, want ErrFixedDurationBudget", err)
	}
}

func TestDurationBudgetRejectsOutOfBandWidths(t *testing.T) {
	runs := []struct {
		name   string
		budget model.DurationBudget
	}{
		{"wider than ±8%", model.DurationBudget{MinMS: 1000, MaxMS: 1500}},
		{"narrower than ±2%", model.DurationBudget{MinMS: 1000, MaxMS: 1001}},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			err := run.budget.Validate()
			if !errors.Is(err, model.ErrDurationBudgetRange) {
				t.Fatalf("got %v, want ErrDurationBudgetRange", err)
			}
		})
	}
}

// Below MinTargetMS the inward rounding collapses both ends onto the same
// millisecond. The band cannot be widened without breaking the ±8% ceiling, so
// the only honest outcome is a rejection rather than a silently clamped value.
func TestNewDurationBudgetRejectsTargetsBelowTheExpressibleMinimum(t *testing.T) {
	for targetMS := int64(1); targetMS < model.MinTargetMS; targetMS++ {
		if err := model.NewDurationBudget(targetMS).Validate(); err == nil {
			t.Fatalf("target %dms produced a usable budget, want a rejection", targetMS)
		}
	}
	// MinTargetMS itself must work, otherwise the constant is wrong.
	if err := model.NewDurationBudget(model.MinTargetMS).Validate(); err != nil {
		t.Fatalf("target %dms should be the first usable one: %v", model.MinTargetMS, err)
	}
}

func TestDurationBudgetRejectsNonPositiveAndInvertedBounds(t *testing.T) {
	if err := (model.DurationBudget{MinMS: 0, MaxMS: 100}).Validate(); err == nil {
		t.Fatal("a zero min_ms must be rejected")
	}
	if err := (model.DurationBudget{MinMS: 1000, MaxMS: 900}).Validate(); err == nil {
		t.Fatal("max_ms below min_ms must be rejected")
	}
}

func TestDurationBudgetContainsBothEnds(t *testing.T) {
	b := model.DurationBudget{MinMS: 920, MaxMS: 1080}
	for _, run := range []struct {
		ms   int64
		want bool
	}{{919, false}, {920, true}, {1000, true}, {1080, true}, {1081, false}} {
		if got := b.Contains(run.ms); got != run.want {
			t.Errorf("Contains(%d) = %v, want %v", run.ms, got, run.want)
		}
	}
}

func TestSegValidateRejectsMalformedFields(t *testing.T) {
	runs := []struct {
		name  string
		mutil func(*model.Seg)
		want  string
	}{
		{"empty id", func(s *model.Seg) { s.SegID = "" }, "seg_id"},
		{"empty text", func(s *model.Seg) { s.Text = "" }, "text"},
		{"unknown emotion", func(s *model.Seg) { s.EmotionTag = "furious" }, "emotion_tag"},
		{"empty emotion", func(s *model.Seg) { s.EmotionTag = "" }, "emotion_tag"},
		{"unknown breath", func(s *model.Seg) { s.Breath = "gasp" }, "breath"},
		{"empty breath", func(s *model.Seg) { s.Breath = "" }, "breath"},
		{"break at zero", func(s *model.Seg) { s.SubtitleBreaks = []int{0} }, "subtitle_breaks"},
		{"break past the text", func(s *model.Seg) { s.SubtitleBreaks = []int{99} }, "subtitle_breaks"},
		{"breaks not increasing", func(s *model.Seg) { s.SubtitleBreaks = []int{3, 3} }, "subtitle_breaks"},
		{"duplicate dependency", func(s *model.Seg) { s.DependsOn = []string{"a", "a"} }, "depends_on"},
		{"empty dependency", func(s *model.Seg) { s.DependsOn = []string{""} }, "depends_on"},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			seg := model.NewSeg("s1", "hello there", 1000)
			run.mutil(&seg)
			err := seg.Validate()
			if err == nil {
				t.Fatalf("%s should have been rejected", run.name)
			}
			if !strings.Contains(err.Error(), run.want) {
				t.Fatalf("error %q does not name the offending field %q", err, run.want)
			}
		})
	}
}

// Subtitle break offsets are rune indices, so a multi-byte script must not push
// a legitimate break past the end of the text.
func TestSegSubtitleBreaksCountRunesNotBytes(t *testing.T) {
	seg := model.NewSeg("s1", "增量重编译", 1000)
	seg.SubtitleBreaks = []int{2, 4}
	if err := seg.Validate(); err != nil {
		t.Fatalf("rune offsets inside a 5-rune string were rejected: %v", err)
	}
	seg.SubtitleBreaks = []int{5}
	if err := seg.Validate(); err == nil {
		t.Fatal("an offset equal to the rune count must be rejected")
	}
}

func TestSegAudioSourceIsOptionalButValidatedWhenPresent(t *testing.T) {
	seg := model.NewSeg("s1", "hello", 1000)
	if err := seg.Validate(); err != nil {
		t.Fatalf("a nil audio_source must be accepted: %v", err)
	}

	seg.AudioSource = &model.AudioSource{Kind: "hummed"}
	if err := seg.Validate(); err == nil {
		t.Fatal("an unknown audio_source.kind must be rejected")
	}

	seg.AudioSource = &model.AudioSource{Kind: model.AudioRecording}
	if err := seg.Validate(); err == nil {
		t.Fatal("a recording without a uri must be rejected")
	}

	seg.AudioSource = &model.AudioSource{Kind: model.AudioRecording, URI: "file:///take3.wav", InPointMS: 500, OutPointMS: 400}
	if err := seg.Validate(); err == nil {
		t.Fatal("an out point before the in point must be rejected")
	}
}

// CanReuse is where the floating budget pays off: the key alone is not enough,
// and a key match with an out-of-band duration must still miss.
func TestCanReuseRequiresBothTheKeyAndTheBudget(t *testing.T) {
	seg := model.NewSeg("s1", "hello", 1000)
	seg.RenderCacheKey = model.ComputeRenderCacheKey(seg, model.RenderProfile{})

	if !seg.CanReuse(seg.RenderCacheKey, 1000) {
		t.Fatal("a matching key with an in-budget duration must be reusable")
	}
	if !seg.CanReuse(seg.RenderCacheKey, seg.DurationBudget.MaxMS) {
		t.Fatal("a duration exactly at the budget ceiling must be reusable")
	}
	if seg.CanReuse(seg.RenderCacheKey, seg.DurationBudget.MaxMS+1) {
		t.Fatal("a duration past the budget ceiling must not be reusable")
	}
	if seg.CanReuse("rk1:something-else", 1000) {
		t.Fatal("a different key must not be reusable")
	}

	unsealed := model.NewSeg("s2", "hello", 1000)
	if unsealed.CanReuse("", 1000) {
		t.Fatal("an unsealed seg must never claim a cache hit")
	}
}
