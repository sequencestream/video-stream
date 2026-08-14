package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/timespan"
)

func TestBuildTrimGraphChainsEveryKeptSpan(t *testing.T) {
	keep := timespan.Ranges{{StartMS: 1500, EndMS: 3000}, {StartMS: 8000, EndMS: 9250}}
	graph := buildTrimGraph(keep, true, true)

	for _, want := range []string{
		"[0:v]trim=start=1.500:end=3.000,setpts=PTS-STARTPTS[v0];",
		"[0:a]atrim=start=1.500:end=3.000,asetpts=PTS-STARTPTS[a0];",
		"[0:v]trim=start=8.000:end=9.250,setpts=PTS-STARTPTS[v1];",
		"[v0][a0][v1][a1]concat=n=2:v=1:a=1[outv][outa]",
	} {
		if !strings.Contains(graph, want) {
			t.Fatalf("graph is missing %q:\n%s", want, graph)
		}
	}
}

// Mapping [0:a] against a file with no audio fails with "Stream specifier
// matches no streams", so the graph must leave audio out entirely.
func TestBuildTrimGraphOmitsMissingStreams(t *testing.T) {
	keep := timespan.Ranges{{StartMS: 0, EndMS: 1000}}

	silent := buildTrimGraph(keep, true, false)
	if strings.Contains(silent, "atrim") || !strings.Contains(silent, "concat=n=1:v=1:a=0[outv]") {
		t.Fatalf("video-only graph is wrong:\n%s", silent)
	}

	audioOnly := buildTrimGraph(keep, false, true)
	if strings.Contains(audioOnly, "[0:v]") || !strings.Contains(audioOnly, "concat=n=1:v=0:a=1[outa]") {
		t.Fatalf("audio-only graph is wrong:\n%s", audioOnly)
	}
}

func TestScaleFilterPerFitMode(t *testing.T) {
	tests := []struct {
		fit  Fit
		want string
	}{
		{FitCover, "scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920"},
		{FitContain, "scale=1080:1920:force_original_aspect_ratio=decrease,pad=1080:1920:(ow-iw)/2:(oh-ih)/2:black"},
		{FitStretch, "scale=1080:1920"},
	}
	for _, tt := range tests {
		if got := ScaleFilter(1080, 1920, tt.fit, ""); got != tt.want {
			t.Fatalf("ScaleFilter(%q) = %q want %q", tt.fit, got, tt.want)
		}
	}
}

// H.264's 4:2:0 chroma cannot represent an odd dimension, and ffmpeg fails
// rather than rounding.
func TestScaleFilterRoundsToEvenDimensions(t *testing.T) {
	got := ScaleFilter(1081, 721, FitStretch, "")
	if got != "scale=1080:720" {
		t.Fatalf("got %q want odd dimensions rounded down", got)
	}
}

func TestAtempoChainStaysInsideTheFilterRange(t *testing.T) {
	tests := map[float64]string{
		1.5:  "atempo=1.5",
		2:    "atempo=2",
		4:    "atempo=2,atempo=2",
		8:    "atempo=2,atempo=2,atempo=2",
		0.5:  "atempo=0.5",
		0.25: "atempo=0.5,atempo=0.5",
	}
	for rate, want := range tests {
		if got := atempoChain(rate); got != want {
			t.Fatalf("atempoChain(%g) = %q want %q", rate, got, want)
		}
	}
}

func TestParseSizeAcceptsNamesAndDimensions(t *testing.T) {
	tests := map[string][2]int{
		"1920x1080": {1920, 1080},
		"720p":      {1280, 720},
		"1080p":     {1920, 1080},
		"4k":        {3840, 2160},
		"vertical":  {1080, 1920},
		"square":    {1080, 1080},
		"640X480":   {640, 480},
	}
	for input, want := range tests {
		w, h, err := ParseSize(input)
		if err != nil {
			t.Fatalf("ParseSize(%q): %v", input, err)
		}
		if w != want[0] || h != want[1] {
			t.Fatalf("ParseSize(%q) = %dx%d want %dx%d", input, w, h, want[0], want[1])
		}
	}
	for _, bad := range []string{"", "huge", "1920", "0x0", "-10x10"} {
		if _, _, err := ParseSize(bad); err == nil {
			t.Fatalf("ParseSize(%q) should have failed", bad)
		}
	}
}

func TestParseFit(t *testing.T) {
	for input, want := range map[string]Fit{
		"": FitCover, "cover": FitCover, "contain": FitContain,
		"pad": FitContain, "stretch": FitStretch, "COVER": FitCover,
	} {
		got, err := ParseFit(input)
		if err != nil || got != want {
			t.Fatalf("ParseFit(%q) = %q,%v want %q", input, got, err, want)
		}
	}
	if _, err := ParseFit("squish"); err == nil {
		t.Fatal("an unknown fit should fail")
	}
}

func TestForceStyleUsesASSAlignmentNumbers(t *testing.T) {
	for position, want := range map[string]string{
		"bottom": "Alignment=2", "center": "Alignment=5", "top": "Alignment=8",
		"": "Alignment=2",
	} {
		style := SubtitleStyle{FontSize: 40, Position: position}
		if got := style.ForceStyle(); !strings.Contains(got, want) {
			t.Fatalf("position %q produced %q want %q", position, got, want)
		}
	}
}

func TestForceStyleOmitsUnsetFields(t *testing.T) {
	got := SubtitleStyle{Position: "bottom"}.ForceStyle()
	if strings.Contains(got, "FontName") || strings.Contains(got, "FontSize") {
		t.Fatalf("unset fields leaked into %q", got)
	}

	// ASS booleans are 0 and -1, and libass ignores Bold=1.
	bold := SubtitleStyle{Bold: true}.ForceStyle()
	if !strings.Contains(bold, "Bold=-1") {
		t.Fatalf("got %q want Bold=-1", bold)
	}
}

func TestSubtitleCodecMatchesTheContainer(t *testing.T) {
	for output, want := range map[string]string{
		"a.mp4": "mov_text", "a.mov": "mov_text", "a.mkv": "srt", "a.webm": "srt",
	} {
		if got := subtitleCodecFor(output); got != want {
			t.Fatalf("subtitleCodecFor(%q) = %q want %q", output, got, want)
		}
	}
}

func TestParseSilenceReadsTheFilterLog(t *testing.T) {
	log := `
[silencedetect @ 0x600] silence_start: 2.001
[silencedetect @ 0x600] silence_end: 5 | silence_duration: 2.999
[silencedetect @ 0x600] silence_start: 7
[silencedetect @ 0x600] silence_end: 10.5 | silence_duration: 3.5
`
	got := parseSilence(log, 15000)
	want := timespan.Ranges{{StartMS: 2001, EndMS: 5000}, {StartMS: 7000, EndMS: 10500}}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

// Silence that runs to the end of the file never gets a silence_end line. That
// is the trailing dead air at the end of a take — exactly the span worth
// trimming — so it must be closed against the duration rather than dropped.
func TestParseSilenceClosesAnUnterminatedRun(t *testing.T) {
	log := "[silencedetect @ 0x1] silence_start: 8.25\n"
	got := parseSilence(log, 12000)
	if len(got) != 1 || got[0] != (timespan.Range{StartMS: 8250, EndMS: 12000}) {
		t.Fatalf("got %v want one range 8250-12000", got)
	}
}

func TestEncodeArgs(t *testing.T) {
	enc := Encode{VideoCodec: "libx264", AudioCodec: "aac", CRF: 18, Preset: "slow", AudioBitrate: "128k"}
	video := strings.Join(enc.VideoArgs(), " ")
	if !strings.Contains(video, "-crf 18") || !strings.Contains(video, "-preset slow") {
		t.Fatalf("video args = %q", video)
	}
	// yuv420p is what browsers, phones and QuickTime can all decode.
	if !strings.Contains(video, "-pix_fmt yuv420p") {
		t.Fatalf("video args = %q want an explicit pixel format", video)
	}
	if audio := strings.Join(enc.AudioArgs(), " "); audio != "-c:a aac -b:a 128k" {
		t.Fatalf("audio args = %q", audio)
	}

	// A quality setting is meaningless for a codec that has no CRF.
	copied := Encode{VideoCodec: "copy"}
	if got := strings.Join(copied.VideoArgs(), " "); got != "-c:v copy" {
		t.Fatalf("copy args = %q", got)
	}
}

func TestFaststartOnlyForMP4Containers(t *testing.T) {
	if got := faststart("out.mp4", "tmp.mp4"); len(got) != 3 || got[0] != "-movflags" {
		t.Fatalf("mp4 should get faststart, got %v", got)
	}
	if got := faststart("out.mkv", "tmp.mkv"); len(got) != 1 || got[0] != "tmp.mkv" {
		t.Fatalf("matroska must not get the mp4 flag, got %v", got)
	}
}

func TestShellQuoteOnlyWhenNeeded(t *testing.T) {
	if got := shellQuote("plain.mp4"); got != "plain.mp4" {
		t.Fatalf("got %q want it left alone", got)
	}
	if got := shellQuote("my file.mp4"); got != "'my file.mp4'" {
		t.Fatalf("got %q want it quoted", got)
	}
	if got := shellQuote("it's.mp4"); !strings.Contains(got, `'\''`) {
		t.Fatalf("got %q want the quote escaped", got)
	}
}

func TestTailKeepsTheEndOfTheOutput(t *testing.T) {
	long := strings.Repeat("noise\n", 500) + "the actual error"
	got := tail(long, 64)
	if !strings.HasSuffix(got, "the actual error") {
		t.Fatalf("got %q want the last line preserved", got)
	}
	if len(got) > 64 {
		t.Fatalf("got %d bytes want at most 64", len(got))
	}
}

func TestCutRefusesAnEmptyKeepList(t *testing.T) {
	tool := Tool{DryRun: true}
	err := tool.Cut(context.Background(), "in.mp4", "out.mp4", nil, Media{DurationMS: 1000}, CutOptions{})
	if err != ErrNothingKept {
		t.Fatalf("err=%v want ErrNothingKept", err)
	}
}

func TestRunAtomicRefusesToClobberByDefault(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "out.mp4")
	if err := writeFile(existing); err != nil {
		t.Fatal(err)
	}

	tool := Tool{}
	err := tool.RunAtomic(context.Background(), existing, func(tmp string) []string { return []string{tmp} })
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err=%v want a refusal to overwrite", err)
	}

	tool.Overwrite = true
	tool.DryRun = true
	if err := tool.RunAtomic(context.Background(), existing, func(tmp string) []string { return []string{tmp} }); err != nil {
		t.Fatalf("with -f it should proceed: %v", err)
	}
}

func TestHasFilterAgainstTheRealBinary(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is not installed")
	}
	tool := Tool{}
	// scale is in every build worth the name; the other cannot exist.
	if !tool.HasFilter(context.Background(), "scale") {
		t.Fatal("scale should be present in any ffmpeg build")
	}
	if tool.HasFilter(context.Background(), "definitely-not-a-filter") {
		t.Fatal("an invented filter must not be reported as present")
	}
}

func writeFile(path string) error {
	return os.WriteFile(path, []byte("x"), 0o644)
}
