package filler

import (
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/transcript"
)

// speech builds a transcript from (text, startMS, endMS) triples, which keeps
// the tests readable when the timing is the point.
func speech(totalMS int64, spec ...any) transcript.Transcript {
	var cue transcript.Cue
	for i := 0; i < len(spec); i += 3 {
		cue.Words = append(cue.Words, transcript.Word{
			Text:    spec[i].(string),
			StartMS: int64(spec[i+1].(int)),
			EndMS:   int64(spec[i+2].(int)),
		})
	}
	if len(cue.Words) > 0 {
		cue.StartMS = cue.Words[0].StartMS
		cue.EndMS = cue.Words[len(cue.Words)-1].EndMS
	}
	return transcript.Transcript{DurationMS: totalMS, Cues: []transcript.Cue{cue}}
}

func plainOptions(totalMS int64) Options {
	opts := DefaultOptions(totalMS)
	// Isolate the rule under test: pauses and padding have their own tests.
	opts.MaxPause = 0
	opts.PadHead, opts.PadTail, opts.MinKeep = 0, 0, 0
	return opts
}

func kinds(cuts []Cut) map[Kind]int {
	out := map[Kind]int{}
	for _, c := range cuts {
		out[c.Kind]++
	}
	return out
}

func TestDetectFindsHesitationSounds(t *testing.T) {
	tr := speech(3000, "嗯", 0, 400, "我", 500, 700, "们", 700, 900, "um", 1000, 1400)

	got, err := Detect(tr, plainOptions(3000))
	if err != nil {
		t.Fatal(err)
	}
	if n := kinds(got.Cuts)[KindFiller]; n != 2 {
		t.Fatalf("got %d filler cuts want 2: %+v", n, got.Cuts)
	}
	for _, c := range got.Cuts {
		if c.Text == "我" || c.Text == "们" {
			t.Fatalf("a real word was cut: %+v", c)
		}
	}
}

// A Chinese filler phrase arrives as one word per character, so matching has to
// join a window of words before comparing.
func TestDetectMatchesPhrasesSplitAcrossWords(t *testing.T) {
	tr := speech(3000, "那", 0, 200, "个", 200, 400, "工", 500, 700, "具", 700, 900)

	opts := plainOptions(3000)
	opts.Aggressive = true
	got, err := Detect(tr, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != 1 {
		t.Fatalf("got %d cuts want one covering 那个: %+v", len(got.Cuts), got.Cuts)
	}
	cut := got.Cuts[0]
	if cut.Text != "那个" || cut.StartMS != 0 || cut.EndMS != 400 {
		t.Fatalf("cut = %+v want 那个 spanning 0-400", cut)
	}
}

// "那个" is a word people mean; cutting it by default would change what was
// said rather than tidy how it was said.
func TestDetectLeavesPaddingWordsAloneWithoutAggressive(t *testing.T) {
	tr := speech(3000, "那", 0, 200, "个", 200, 400, "工", 500, 700, "具", 700, 900)

	got, err := Detect(tr, plainOptions(3000))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != 0 {
		t.Fatalf("got %+v want nothing cut by default", got.Cuts)
	}
}

func TestKeepWordsRemovesAPhraseFromTheVocabulary(t *testing.T) {
	tr := speech(2000, "嗯", 0, 400, "好", 500, 700)

	opts := plainOptions(2000)
	opts.Keep = []string{"嗯"}
	got, err := Detect(tr, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != 0 {
		t.Fatalf("got %+v want 嗯 spared", got.Cuts)
	}
}

func TestOnlyReplacesTheVocabularyEntirely(t *testing.T) {
	tr := speech(2000, "嗯", 0, 400, "好", 500, 700)

	opts := plainOptions(2000)
	opts.Only = []string{"好"}
	got, err := Detect(tr, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != 1 || got.Cuts[0].Text != "好" {
		t.Fatalf("got %+v want only 好 cut", got.Cuts)
	}
}

func TestDetectCutsTheFirstOfAStutteredPair(t *testing.T) {
	tr := speech(3000, "很", 1000, 1200, "很", 1250, 1450, "好", 1500, 1700)

	got, err := Detect(tr, plainOptions(3000))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != 1 {
		t.Fatalf("got %d cuts want 1: %+v", len(got.Cuts), got.Cuts)
	}
	cut := got.Cuts[0]
	if cut.Kind != KindRepeat {
		t.Fatalf("kind=%q want repeat", cut.Kind)
	}
	// The second attempt is the one the speaker finished, so the first goes.
	if cut.StartMS != 1000 || cut.EndMS != 1250 {
		t.Fatalf("cut = %+v want the first instance removed", cut)
	}
}

// Repetition across a breath is emphasis, not a stutter.
func TestDetectLeavesDeliberateRepetitionAlone(t *testing.T) {
	tr := speech(4000, "好", 1000, 1200, "好", 2500, 2700)

	got, err := Detect(tr, plainOptions(4000))
	if err != nil {
		t.Fatal(err)
	}
	if n := kinds(got.Cuts)[KindRepeat]; n != 0 {
		t.Fatalf("got %+v want no repeat cut across a 1.3s gap", got.Cuts)
	}
}

func TestDetectShortensLongPausesWithoutClosingThem(t *testing.T) {
	tr := speech(10000, "一", 0, 500, "二", 5000, 5500)

	opts := plainOptions(10000)
	opts.MaxPause = 700 * time.Millisecond
	got, err := Detect(tr, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != 1 || got.Cuts[0].Kind != KindPause {
		t.Fatalf("got %+v want one pause cut", got.Cuts)
	}
	// The 4.5s gap must end up as 700ms, not zero: speech spliced to no gap at
	// all sounds like a machine did it.
	gap := 4500 - got.Cuts[0].Duration()
	if gap != 700 {
		t.Fatalf("the surviving pause is %dms want 700", gap)
	}
	if got.Cuts[0].StartMS <= 500 || got.Cuts[0].EndMS >= 5000 {
		t.Fatalf("the cut %+v should sit strictly inside the gap", got.Cuts[0])
	}
}

func TestTrimEndsIsOptional(t *testing.T) {
	tr := speech(10000, "一", 3000, 3500)

	opts := plainOptions(10000)
	opts.MaxPause = 500 * time.Millisecond
	if got, _ := Detect(tr, opts); len(got.Cuts) != 0 {
		t.Fatalf("got %+v want the leading and trailing silence left alone", got.Cuts)
	}

	opts.TrimEnds = true
	got, err := Detect(tr, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cuts) != 2 {
		t.Fatalf("got %d cuts want the head and the tail: %+v", len(got.Cuts), got.Cuts)
	}
}

func TestPaddingPullsCutsAwayFromNeighbouringSpeech(t *testing.T) {
	tr := speech(3000, "我", 0, 500, "嗯", 500, 900, "好", 900, 1400)

	opts := plainOptions(3000)
	opts.PadHead = 50 * time.Millisecond
	opts.PadTail = 50 * time.Millisecond
	got, err := Detect(tr, opts)
	if err != nil {
		t.Fatal(err)
	}
	// The reported cut is the word itself; the applied one is narrower.
	if got.Cuts[0].StartMS != 500 || got.Cuts[0].EndMS != 900 {
		t.Fatalf("the report should name the word's own span, got %+v", got.Cuts[0])
	}
	if len(got.Keep) != 2 {
		t.Fatalf("got %d kept ranges want 2: %+v", len(got.Keep), got.Keep)
	}
	if got.Keep[0].EndMS != 550 || got.Keep[1].StartMS != 850 {
		t.Fatalf("keep = %+v want the cut narrowed by 50ms on each side", got.Keep)
	}
}

func TestMinKeepDropsSpeechIslands(t *testing.T) {
	// A 100ms sliver survives between two cuts; it reads as a glitch.
	tr := speech(3000, "嗯", 0, 500, "啊", 600, 1100, "好", 1200, 2000)

	opts := plainOptions(3000)
	opts.MinKeep = 200 * time.Millisecond
	got, err := Detect(tr, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.Keep {
		if r.Duration() < 200 {
			t.Fatalf("keep = %+v contains a fragment shorter than min-keep", got.Keep)
		}
	}
}

func TestKeepAndCutsAccountForTheWholeSource(t *testing.T) {
	tr := speech(6000, "嗯", 0, 400, "我", 500, 700, "很", 3000, 3200, "很", 3250, 3450, "好", 3500, 3700)

	got, err := Detect(tr, DefaultOptions(6000))
	if err != nil {
		t.Fatal(err)
	}
	if got.OutputMS+got.RemovedMS != got.SourceMS {
		t.Fatalf("%d + %d != %d", got.OutputMS, got.RemovedMS, got.SourceMS)
	}
	if got.OutputMS != got.Keep.Total() {
		t.Fatalf("output=%d but the keep list totals %d", got.OutputMS, got.Keep.Total())
	}
	if got.Ratio() <= 0 || got.Ratio() >= 1 {
		t.Fatalf("ratio=%v want a fraction", got.Ratio())
	}
}

func TestDetectNeedsWordTimings(t *testing.T) {
	tr := transcript.Transcript{
		DurationMS: 5000,
		Cues:       []transcript.Cue{{StartMS: 0, EndMS: 5000, Text: "没有词级时间戳"}},
	}
	if _, err := Detect(tr, DefaultOptions(5000)); err == nil {
		t.Fatal("a cue-only transcript cannot be cut precisely and must say so")
	}
}

func TestDetectNeedsADuration(t *testing.T) {
	tr := speech(0, "嗯", 0, 400)
	if _, err := Detect(tr, Options{}); err == nil {
		t.Fatal("without a source duration there is nothing to cut against")
	}
}

func TestNormalizeStripsPunctuationAndCase(t *testing.T) {
	if normalize("Um,") != "um" {
		t.Fatalf("normalize(%q) = %q", "Um,", normalize("Um,"))
	}
	if normalize("你好。") != "你好" {
		t.Fatalf("normalize dropped the wrong characters: %q", normalize("你好。"))
	}
}
