package transcript

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/timespan"
)

// words builds a cue whose words are evenly spaced, which is enough for the
// layout decisions under test.
func words(startMS, perWordMS int64, texts ...string) Cue {
	cue := Cue{StartMS: startMS}
	at := startMS
	for _, text := range texts {
		cue.Words = append(cue.Words, Word{Text: text, StartMS: at, EndMS: at + perWordMS})
		at += perWordMS
	}
	cue.EndMS = at
	cue.Text = JoinText(texts)
	return cue
}

func TestJoinTextSpacesLatinButNotCJK(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  string
	}{
		{"chinese runs together", []string{"我", "们", "今", "天"}, "我们今天"},
		{"english gets spaces", []string{"hello", "world"}, "hello world"},
		{"punctuation stays attached", []string{"hello", ",", "world"}, "hello, world"},
		{"mixed scripts do not gain a space at the boundary", []string{"用", "ffmpeg"}, "用ffmpeg"},
		{"empty parts are dropped", []string{"", "a", "  ", "b"}, "a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := JoinText(tt.parts); got != tt.want {
				t.Fatalf("JoinText(%q) = %q want %q", tt.parts, got, tt.want)
			}
		})
	}
}

func TestWrapLinesBalancesRatherThanFillingGreedily(t *testing.T) {
	// 22 characters over two lines of at most 20: a greedy fill would produce
	// 20 + 2, which reads as broken.
	text := strings.Repeat("字", 22)
	lines := WrapLines(text, 20, 2)
	if len(lines) != 2 {
		t.Fatalf("got %d lines want 2: %q", len(lines), lines)
	}
	if len(lines[1]) < len(lines[0])/2 {
		t.Fatalf("lines are unbalanced: %d and %d characters", len([]rune(lines[0])), len([]rune(lines[1])))
	}
}

// Respecting a line-count preference is never worth dropping a speaker's words.
func TestWrapLinesRelaxesRatherThanTruncating(t *testing.T) {
	text := strings.Repeat("字", 60)
	lines := WrapLines(text, 10, 2)
	if joined := strings.Join(lines, ""); joined != text {
		t.Fatalf("text was lost: %d of %d characters survived", len([]rune(joined)), len([]rune(text)))
	}
	if len(lines) > 2 {
		t.Fatalf("got %d lines want at most 2 after widening: %q", len(lines), lines)
	}
}

func TestWrapLinesBreaksLatinOnWordBoundaries(t *testing.T) {
	lines := WrapLines("the quick brown fox jumps", 12, 3)
	for _, line := range lines {
		if strings.HasPrefix(line, " ") || strings.HasSuffix(line, " ") {
			t.Fatalf("line %q has stray spaces", line)
		}
	}
	if joined := strings.Join(lines, " "); joined != "the quick brown fox jumps" {
		t.Fatalf("words were mangled: %q", joined)
	}
}

func TestSubtitlesBreakOnPausesAndSentenceEnds(t *testing.T) {
	tr := Transcript{Cues: []Cue{{
		StartMS: 0, EndMS: 6000,
		Words: []Word{
			{Text: "第", StartMS: 0, EndMS: 200},
			{Text: "一。", StartMS: 200, EndMS: 400}, // sentence end forces a break
			{Text: "第", StartMS: 500, EndMS: 700},
			{Text: "二", StartMS: 700, EndMS: 900},
			// A one-second gap: a reader expects the text to change here.
			{Text: "第", StartMS: 2000, EndMS: 2200},
			{Text: "三", StartMS: 2200, EndMS: 2400},
		},
	}}}

	subs := tr.Subtitles(DefaultLineOptions(20, 2))
	if len(subs) != 3 {
		t.Fatalf("got %d captions want 3: %+v", len(subs), subs)
	}
	if subs[0].Text() != "第一。" {
		t.Fatalf("first caption = %q want it to end at the full stop", subs[0].Text())
	}
	if subs[2].StartMS != 2000 {
		t.Fatalf("third caption starts at %d want 2000, after the pause", subs[2].StartMS)
	}
	for i, s := range subs {
		if s.Index != i+1 {
			t.Fatalf("caption %d has index %d", i, s.Index)
		}
	}
}

// A caption too short to read is stretched, but never over the next one: two
// captions on screen at once is worse than one that flashes.
func TestSubtitlesEnforceMinimumDurationWithoutOverlapping(t *testing.T) {
	tr := Transcript{Cues: []Cue{
		{StartMS: 0, EndMS: 200, Text: "短"},
		{StartMS: 400, EndMS: 3000, Text: "长一点的一句"},
	}}
	subs := tr.Subtitles(LineOptions{MaxChars: 20, MaxLines: 2, MinDurationMS: 1000})
	if len(subs) != 2 {
		t.Fatalf("got %d captions want 2", len(subs))
	}
	if subs[0].EndMS > subs[1].StartMS {
		t.Fatalf("captions overlap: %d > %d", subs[0].EndMS, subs[1].StartMS)
	}
	if subs[0].EndMS != 400 {
		t.Fatalf("first caption ends at %d want it stretched to the next start", subs[0].EndMS)
	}
}

func TestWriteSRTAndVTTUseTheRightSeparator(t *testing.T) {
	subs := []Subtitle{{StartMS: 1500, EndMS: 3250, Lines: []string{"一行", "二行"}}}

	var srt bytes.Buffer
	if err := WriteSRT(&srt, subs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(srt.String(), "00:00:01,500 --> 00:00:03,250") {
		t.Fatalf("SubRip needs a comma before the milliseconds:\n%s", srt.String())
	}
	if !strings.Contains(srt.String(), "一行\n二行") {
		t.Fatalf("lines should be newline-separated:\n%s", srt.String())
	}

	var vtt bytes.Buffer
	if err := WriteVTT(&vtt, subs); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(vtt.String(), "WEBVTT") {
		t.Fatalf("WebVTT needs its header:\n%s", vtt.String())
	}
	if !strings.Contains(vtt.String(), "00:00:01.500 --> 00:00:03.250") {
		t.Fatalf("WebVTT uses a period:\n%s", vtt.String())
	}
}

func TestKeepRebasesSurvivingWords(t *testing.T) {
	tr := Transcript{Cues: []Cue{words(0, 100, "a", "b", "c", "d", "e")}} // 0..500ms
	// Drop 100-300ms, which is words b and c.
	keep := timespan.Ranges{{StartMS: 0, EndMS: 100}, {StartMS: 300, EndMS: 500}}

	got := tr.Keep(keep)
	if len(got.Cues) != 1 {
		t.Fatalf("got %d cues want 1", len(got.Cues))
	}
	kept := got.Cues[0].Words
	if len(kept) != 3 {
		t.Fatalf("got %d words want a, d, e: %+v", len(kept), kept)
	}
	if kept[0].Text != "a" || kept[1].Text != "d" || kept[2].Text != "e" {
		t.Fatalf("wrong words survived: %+v", kept)
	}
	// d started at 300ms in the source and 100ms of source time precedes it in
	// the output, so it must now start at 100ms.
	if kept[1].StartMS != 100 {
		t.Fatalf("word d starts at %d want 100 after the cut", kept[1].StartMS)
	}
	if got.DurationMS != 300 {
		t.Fatalf("duration=%d want 300", got.DurationMS)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := Transcript{
		Version: Version, Language: "zh", Model: "faster-whisper:small",
		DurationMS: 5000,
		Cues:       []Cue{words(0, 200, "你", "好")},
	}

	var buf bytes.Buffer
	if err := original.Encode(&buf); err != nil {
		t.Fatal(err)
	}
	// Chinese must stay readable in the file: a transcript is something people
	// open and edit by hand.
	if !strings.Contains(buf.String(), "你") {
		t.Fatalf("JSON escaped the text away:\n%s", buf.String())
	}

	got, err := Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "zh" || len(got.Words()) != 2 || got.Words()[1].Text != "好" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestDecodeRejectsAnUnknownSchemaVersion(t *testing.T) {
	_, err := Decode(strings.NewReader(`{"version":"vs.transcript.v9","cues":[]}`))
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("err=%v want a version complaint", err)
	}
}

func TestValidateRejectsBackwardsTimings(t *testing.T) {
	tr := Transcript{Cues: []Cue{{StartMS: 5000, EndMS: 1000}}}
	if err := tr.Validate(); err == nil {
		t.Fatal("a cue ending before it starts should be rejected")
	}
}

func TestHasWordTimings(t *testing.T) {
	with := Transcript{Cues: []Cue{words(0, 100, "a")}}
	if !with.HasWordTimings() {
		t.Fatal("a transcript with words should report word timings")
	}
	without := Transcript{Cues: []Cue{{StartMS: 0, EndMS: 100, Text: "a"}}}
	if without.HasWordTimings() {
		t.Fatal("a cue-only transcript must not claim word timings")
	}
	if (Transcript{}).HasWordTimings() {
		t.Fatal("an empty transcript must not claim word timings")
	}
}

func TestFormatForPath(t *testing.T) {
	tests := map[string]Format{
		"a.srt": FormatSRT, "a.vtt": FormatVTT, "a.txt": FormatText,
		"a.json": FormatJSON, "a": FormatJSON,
	}
	for path, want := range tests {
		if got := FormatForPath(path); got != want {
			t.Fatalf("FormatForPath(%q) = %q want %q", path, got, want)
		}
	}
}
