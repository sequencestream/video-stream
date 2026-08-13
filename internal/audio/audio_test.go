package audio

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
)

func TestPlaybackRateWithinBudget(t *testing.T) {
	budget := model.NewDurationBudget(5000)
	rate, err := PlaybackRate(5000, budget)
	if err != nil || rate != 1.0 {
		t.Fatalf("rate=%v err=%v", rate, err)
	}
}

func TestPlaybackRateStretchWithin8Percent(t *testing.T) {
	budget := model.NewDurationBudget(5000)
	// Just above MaxMS; minimal stretch should land inside budget.
	rate, err := PlaybackRate(budget.MaxMS+50, budget)
	if err != nil {
		t.Fatal(err)
	}
	if rate <= 1.0 || rate > 1.08 {
		t.Fatalf("rate=%v want (1,1.08]", rate)
	}
	adj := AdjustedDurationMS(budget.MaxMS+50, rate)
	if !budget.Contains(adj) {
		t.Fatalf("adjusted %d not in budget [%d,%d]", adj, budget.MinMS, budget.MaxMS)
	}
}

func TestPlaybackRateRejectsOutsideBudget(t *testing.T) {
	budget := model.NewDurationBudget(5000)
	_, err := PlaybackRate(budget.MaxMS*2, budget)
	if !errors.Is(err, ErrNeedsWordCountChange) {
		t.Fatalf("err=%v want ErrNeedsWordCountChange", err)
	}
}

func TestStubTTSSynthesizes(t *testing.T) {
	seg := model.NewSeg("s1", "one two three four five six seven eight nine ten eleven twelve", 3000)
	res, err := StubTTS{MSPerWord: 250}.Synthesize(t.Context(), seg, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tokens) != 12 {
		t.Fatalf("tokens=%d want 12", len(res.Tokens))
	}
	if !seg.DurationBudget.Contains(res.ActualMS) {
		t.Fatalf("actual %d outside budget", res.ActualMS)
	}
}

func TestLongStubTTSRejects(t *testing.T) {
	seg := model.NewSeg("s1", "too long", 2000)
	_, err := LongStubTTS{}.Synthesize(t.Context(), seg, "")
	if !errors.Is(err, ErrNeedsWordCountChange) {
		t.Fatalf("err=%v", err)
	}
}

func TestNormalizeLUFSWithinTolerance(t *testing.T) {
	gain, ok := NormalizeLUFS(-14.2, -14, 0.5)
	if !ok || gain != 0 {
		t.Fatalf("gain=%v ok=%v", gain, ok)
	}
}

func TestNormalizeLUFSReturnsRequiredGainOutsideTolerance(t *testing.T) {
	gain, ok := NormalizeLUFS(-24, -14, 0.5)
	if ok || gain != 10 {
		t.Fatalf("gain=%v ok=%v", gain, ok)
	}
}

func TestEngineSynthesizeSoftAndBurnIn(t *testing.T) {
	dir := t.TempDir()
	p := model.NewProject("p1", "t", testNow())
	p.Segs = []model.Seg{model.NewSeg("hook", "one two three four five six seven eight nine ten eleven twelve thirteen", 4000)}
	p.Seal()

	eng := New(Options{OutputDir: dir, TTS: StubTTS{MSPerWord: 300}})

	for _, mode := range []SubtitleMode{SubtitleSoft, SubtitleBurnIn} {
		res, err := eng.Synthesize(t.Context(), SynthesizeRequest{
			Project: p, Platform: "youtube", Mode: mode,
		})
		if err != nil {
			t.Fatalf("mode=%s: %v", mode, err)
		}
		if res.AudioURI == "" || res.SubtitleURI == "" {
			t.Fatalf("missing outputs: %+v", res)
		}
		body, readErr := os.ReadFile(res.SubtitleURI)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.HasPrefix(string(body), "WEBVTT\n\n") || strings.Count(string(body), "WEBVTT") != 1 {
			t.Fatalf("mode=%s produced invalid WebVTT: %q", mode, body)
		}
		if res.MeasuredLUFS < res.TargetLUFS-0.5 || res.MeasuredLUFS > res.TargetLUFS+0.5 {
			t.Fatalf("lufs=%v target=%v", res.MeasuredLUFS, res.TargetLUFS)
		}
	}
}

func TestSegmentSubtitleUsesRuneBreaksAndChineseLineLimits(t *testing.T) {
	seg := model.NewSeg("s1", "你好世界字幕", 2000)
	seg.SubtitleBreaks = []int{2}
	tokens := allocateTokens(seg.SegID, []string{"你好世界字幕"}, 2000)
	lines := SegmentSubtitle(seg, tokens, PlatformSpec{MaxCharsPerLine: 2})
	want := []string{"你好", "世界", "字幕"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines=%q want %q", lines, want)
	}
}

func TestFormatWebVTTCueHasNoRepeatedHeader(t *testing.T) {
	got := FormatWebVTT("seg-1", []string{"hello"}, 0, 1250)
	if strings.Contains(got, "WEBVTT") || got != "seg-1\n00:00:00.000 --> 00:00:01.250\nhello\n" {
		t.Fatalf("cue=%q", got)
	}
}

func TestSpecForPlatform(t *testing.T) {
	spec, ok := SpecFor("douyin")
	if !ok || spec.MaxCharsPerLine != 18 {
		t.Fatalf("spec=%+v ok=%v", spec, ok)
	}
}
