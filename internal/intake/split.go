package intake

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Splitting bounds in runes. A seg is one spoken breath group: long enough that
// the render cache has something worth reusing, short enough that editing one
// sentence does not invalidate half the video.
//
// The bounds count runes rather than bytes because the scripts this reads are
// mostly CJK, where a "character" is one rune and one syllable.
const (
	// DefaultMaxRunes is where an over-long sentence gets split at its last
	// comma. Roughly 12 seconds of Mandarin narration.
	DefaultMaxRunes = 48
	// DefaultMinRunes is the shortest standalone seg. Anything shorter is
	// merged into its predecessor: a two-word seg is its own render, its own
	// cache entry, and its own crossfade, which buys nothing.
	DefaultMinRunes = 8
)

// sentenceEnders close a sentence and stay attached to it.
var sentenceEnders = []rune{'。', '！', '？', '；', '!', '?', ';', '…'}

// clauseBreaks are where an over-long sentence may be cut. The cut keeps the
// mark on the leading half, so no punctuation is lost.
var clauseBreaks = []rune{'，', ',', '、', '：', ':', '—'}

// Split turns narration prose into seg-sized lines.
//
// The rules, in order: break on sentence-ending punctuation, cut anything still
// over maxRunes at its latest clause break, then merge anything under minRunes
// into the line before it. Punctuation is never dropped — the text is handed to
// a TTS engine, which reads pauses off exactly those marks.
func Split(text string, maxRunes, minRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = DefaultMaxRunes
	}
	if minRunes <= 0 {
		minRunes = DefaultMinRunes
	}

	sentences := splitSentences(text)
	var wrapped []string
	for _, s := range sentences {
		wrapped = append(wrapped, splitLong(s, maxRunes)...)
	}
	return mergeShort(wrapped, minRunes, maxRunes)
}

// splitSentences breaks on sentence-ending punctuation and on blank lines.
func splitSentences(text string) []string {
	var out []string
	var current strings.Builder
	flush := func() {
		if s := strings.TrimSpace(current.String()); s != "" {
			out = append(out, s)
		}
		current.Reset()
	}

	for _, r := range text {
		if r == '\n' || r == '\r' {
			flush()
			continue
		}
		current.WriteRune(r)
		if slices.Contains(sentenceEnders, r) {
			flush()
		}
	}
	flush()
	return out
}

// splitLong cuts an over-long line at the last clause break that leaves both
// halves usable, then recurses on the tail.
func splitLong(sentence string, maxRunes int) []string {
	if utf8.RuneCountInString(sentence) <= maxRunes {
		return []string{sentence}
	}

	runes := []rune(sentence)
	cut := -1
	for i := 0; i < len(runes) && i < maxRunes; i++ {
		if slices.Contains(clauseBreaks, runes[i]) {
			cut = i
		}
	}
	if cut < 0 {
		// No clause break to cut at. A hard cut mid-phrase would hand the TTS
		// engine a fragment it reads with the wrong intonation, so the line is
		// left over-long instead: too long is survivable, mis-read is not.
		return []string{sentence}
	}

	head := strings.TrimSpace(string(runes[:cut+1]))
	tail := strings.TrimSpace(string(runes[cut+1:]))
	if head == "" || tail == "" {
		return []string{sentence}
	}
	return append([]string{head}, splitLong(tail, maxRunes)...)
}

// mergeShort folds runt lines into their predecessor, but never past maxRunes:
// a merge that overshoots the cap just recreates the problem splitLong solved.
func mergeShort(lines []string, minRunes, maxRunes int) []string {
	var out []string
	for _, line := range lines {
		if len(out) == 0 {
			out = append(out, line)
			continue
		}
		prev := out[len(out)-1]
		merged := joinLines(prev, line)
		tooShort := utf8.RuneCountInString(line) < minRunes
		if tooShort && utf8.RuneCountInString(merged) <= maxRunes {
			out[len(out)-1] = merged
			continue
		}
		out = append(out, line)
	}
	// A leading runt has no predecessor to merge into, so it merges forward.
	if len(out) > 1 && utf8.RuneCountInString(out[0]) < minRunes {
		if merged := joinLines(out[0], out[1]); utf8.RuneCountInString(merged) <= maxRunes {
			out = append([]string{merged}, out[2:]...)
		}
	}
	return out
}

// joinLines glues two lines, inserting a space only between two ASCII words:
// CJK text has no word spacing and a space would be read as a pause.
func joinLines(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	last, _ := utf8.DecodeLastRuneInString(a)
	first, _ := utf8.DecodeRuneInString(b)
	if isASCIIWord(last) && isASCIIWord(first) {
		return a + " " + b
	}
	return a + b
}

func isASCIIWord(r rune) bool {
	return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r))
}
