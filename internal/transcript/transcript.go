// Package transcript is the interchange format between vs commands.
//
// Every command that needs to know what was said reads a Transcript, and
// `vs transcribe` is the only command that produces one. That split is what
// makes the tool composable: recognition is the slow, expensive step, so
// running it once and reusing the JSON across a subtitle pass, a filler pass
// and a cut pass is the difference between one model run and three.
//
// Timestamps are milliseconds against the source media's timeline, stored as
// integers. Floating-point seconds accumulate rounding error once you start
// shifting cues across a cut, and a subtitle that drifts 40 ms late is visible.
package transcript

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/sequencestream/video-stream/internal/timespan"
)

// Version tags the JSON schema. It is checked on load so a transcript written
// by an incompatible future build fails loudly instead of being misread.
const Version = "vs.transcript.v1"

// Word is one recognized token with its timing.
type Word struct {
	Text    string  `json:"text"`
	StartMS int64   `json:"start_ms"`
	EndMS   int64   `json:"end_ms"`
	Score   float64 `json:"score,omitempty"`
}

// Duration is the word's length in milliseconds.
func (w Word) Duration() int64 { return w.EndMS - w.StartMS }

// Cue is one contiguous utterance as the recognizer segmented it.
//
// Cue boundaries are the recognizer's opinion about where speech pauses, not a
// subtitle layout decision. Building subtitle lines re-splits them; see Lines.
type Cue struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
	Words   []Word `json:"words,omitempty"`
}

// Duration is the cue's length in milliseconds.
func (c Cue) Duration() int64 { return c.EndMS - c.StartMS }

// Transcript is a full recognition result for one media file.
type Transcript struct {
	Version    string `json:"version"`
	Source     string `json:"source,omitempty"`
	Language   string `json:"language,omitempty"`
	Model      string `json:"model,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Cues       []Cue  `json:"cues"`
}

// Words flattens every cue's words in timeline order.
//
// Filler detection works on this view rather than on cues: a disfluency is a
// word, and the pause worth cutting is the gap between two words, which may
// well sit inside a cue the recognizer thought was one utterance.
func (t Transcript) Words() []Word {
	var out []Word
	for _, c := range t.Cues {
		out = append(out, c.Words...)
	}
	return out
}

// HasWordTimings reports whether every cue carries word-level timing.
//
// Filler removal and karaoke-style subtitles are impossible without it, and
// failing early with that sentence beats silently producing a video where
// nothing was cut.
func (t Transcript) HasWordTimings() bool {
	for _, c := range t.Cues {
		if len(c.Words) == 0 {
			return false
		}
	}
	return len(t.Cues) > 0
}

// Text is the full transcript as running prose.
func (t Transcript) Text() string {
	parts := make([]string, 0, len(t.Cues))
	for _, c := range t.Cues {
		if s := strings.TrimSpace(c.Text); s != "" {
			parts = append(parts, s)
		}
	}
	return JoinText(parts)
}

// Span is the interval the transcript covers.
func (t Transcript) Span() timespan.Range {
	if len(t.Cues) == 0 {
		return timespan.Range{}
	}
	return timespan.Range{StartMS: t.Cues[0].StartMS, EndMS: t.Cues[len(t.Cues)-1].EndMS}
}

// Sort orders cues and their words by start time. Recognizers emit sorted
// output, but a hand-edited transcript is a supported input.
func (t *Transcript) Sort() {
	sort.SliceStable(t.Cues, func(i, j int) bool { return t.Cues[i].StartMS < t.Cues[j].StartMS })
	for i := range t.Cues {
		words := t.Cues[i].Words
		sort.SliceStable(words, func(a, b int) bool { return words[a].StartMS < words[b].StartMS })
	}
}

// Validate rejects transcripts that would produce nonsense downstream.
func (t Transcript) Validate() error {
	if t.Version != "" && t.Version != Version {
		return fmt.Errorf("transcript version %q is not %q", t.Version, Version)
	}
	for i, c := range t.Cues {
		if c.EndMS < c.StartMS {
			return fmt.Errorf("cue %d ends (%dms) before it starts (%dms)", i, c.EndMS, c.StartMS)
		}
		if c.StartMS < 0 {
			return fmt.Errorf("cue %d starts before zero (%dms)", i, c.StartMS)
		}
		for j, w := range c.Words {
			if w.EndMS < w.StartMS {
				return fmt.Errorf("cue %d word %d ends (%dms) before it starts (%dms)", i, j, w.EndMS, w.StartMS)
			}
		}
	}
	return nil
}

// Load reads a transcript from a JSON file, or from stdin when path is "-".
func Load(path string) (Transcript, error) {
	if path == "-" {
		return Decode(os.Stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return Transcript{}, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	return Decode(f)
}

// Decode reads a transcript from r.
func Decode(r io.Reader) (Transcript, error) {
	var t Transcript
	dec := json.NewDecoder(r)
	if err := dec.Decode(&t); err != nil {
		return Transcript{}, fmt.Errorf("parse transcript: %w", err)
	}
	if err := t.Validate(); err != nil {
		return Transcript{}, err
	}
	t.Sort()
	return t, nil
}

// Encode writes the transcript as indented JSON.
func (t Transcript) Encode(w io.Writer) error {
	if t.Version == "" {
		t.Version = Version
	}
	if t.Cues == nil {
		t.Cues = []Cue{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(t)
}

// WriteFile writes the transcript to path, creating parent directories.
func (t Transcript) WriteFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("transcript output path is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := t.Encode(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// JoinText concatenates fragments the way their script wants to be read: with
// spaces between Latin words, without them between CJK characters.
//
// Getting this wrong is not cosmetic. "我 们 今 天" wraps at every character
// when a subtitle line is measured, and "helloworld" is simply a different
// word than "hello world".
func JoinText(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b.Len() > 0 && needsSpace(lastRune(b.String()), firstRune(p)) {
			b.WriteByte(' ')
		}
		b.WriteString(p)
	}
	return b.String()
}

// needsSpace reports whether a separator belongs between two adjacent runes.
func needsSpace(prev, next rune) bool {
	if prev == 0 || next == 0 {
		return false
	}
	if unicode.IsSpace(prev) || unicode.IsSpace(next) {
		return false
	}
	if isCJK(prev) || isCJK(next) {
		return false
	}
	// Never push punctuation away from the word it attaches to.
	if unicode.IsPunct(next) && !isOpeningPunct(next) {
		return false
	}
	return true
}

func isOpeningPunct(r rune) bool {
	switch r {
	case '(', '[', '{', '"', '\'', '¿', '¡':
		return true
	default:
		return false
	}
}

// isCJK covers the scripts written without inter-word spaces.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		(r >= 0x3000 && r <= 0x303F) || // CJK punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // full-width forms
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	var last rune
	for _, r := range s {
		last = r
	}
	return last
}
