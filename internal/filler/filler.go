// Package filler finds the parts of a take that carry no content: hesitation
// sounds, stuttered repeats, and pauses longer than a listener will sit through.
//
// It only ever produces a list of spans. Nothing here touches a media file, so
// the decision of what to cut can be printed, reviewed and argued with before
// anything is re-encoded — which matters, because an over-eager filler pass
// produces a video that is subtly wrong in a way that is hard to point at.
package filler

import (
	"errors"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/timespan"
	"github.com/sequencestream/video-stream/internal/transcript"
)

// Kind labels why a span was marked for removal.
type Kind string

// The reasons a span gets cut.
const (
	// KindFiller is a hesitation sound or a padding phrase.
	KindFiller Kind = "filler"
	// KindRepeat is a stuttered word said twice in a row.
	KindRepeat Kind = "repeat"
	// KindPause is dead air beyond what a pause needs to be.
	KindPause Kind = "pause"
)

// Cut is one span marked for removal, with the reason attached.
type Cut struct {
	timespan.Range
	Kind Kind   `json:"kind"`
	Text string `json:"text,omitempty"`
}

// Label describes the cut in one phrase: the reason, and the text it applies
// to. Layout is the caller's problem — aligning a column of these needs display
// widths, and a Chinese character is two columns wide.
func (c Cut) Label() string {
	if c.Text == "" {
		return string(c.Kind)
	}
	return string(c.Kind) + " " + c.Text
}

// Options controls what counts as worth cutting.
type Options struct {
	// Extra adds phrases to the filler vocabulary.
	Extra []string
	// Keep removes phrases from it, for a speaker whose "嗯" means something.
	Keep []string
	// Aggressive adds the real words used as verbal padding. Off by default:
	// see AggressiveWords for why.
	Aggressive bool
	// Only replaces the built-in vocabulary entirely.
	Only []string
	// Repeats removes the first of two identical words said back to back.
	Repeats bool
	// MaxPause is the longest silence left intact. Longer gaps are shortened
	// to it rather than closed completely, because speech spliced to zero gap
	// sounds like a machine did it.
	MaxPause time.Duration
	// TrimEnds applies the same limit to the silence before the first word and
	// after the last one.
	TrimEnds bool
	// PadHead and PadTail shrink every cut so it cannot clip the release of the
	// word before it or the attack of the word after.
	PadHead, PadTail time.Duration
	// MinKeep drops surviving fragments shorter than this.
	MinKeep time.Duration
	// TotalMS is the source duration. Required: without it there is no way to
	// know what "everything else" means.
	TotalMS int64
}

// DefaultOptions returns the settings a first run should use.
func DefaultOptions(totalMS int64) Options {
	return Options{
		Repeats:  true,
		MaxPause: 700 * time.Millisecond,
		PadHead:  60 * time.Millisecond,
		PadTail:  80 * time.Millisecond,
		MinKeep:  200 * time.Millisecond,
		TotalMS:  totalMS,
	}
}

// Result is the full plan: what to remove, what survives, and by how much.
type Result struct {
	Cuts       []Cut           `json:"cuts"`
	Keep       timespan.Ranges `json:"keep"`
	SourceMS   int64           `json:"source_ms"`
	OutputMS   int64           `json:"output_ms"`
	RemovedMS  int64           `json:"removed_ms"`
	Counts     map[string]int  `json:"counts"`
	Vocabulary []string        `json:"-"`
}

// Ratio is the fraction of the source that would be removed.
func (r Result) Ratio() float64 {
	if r.SourceMS <= 0 {
		return 0
	}
	return float64(r.RemovedMS) / float64(r.SourceMS)
}

// ErrNoWordTimings means the transcript lacks the per-word timing this needs.
var ErrNoWordTimings = errors.New("this transcript has no word-level timings, so nothing can be located precisely enough to cut")

// Detect builds the cut plan for a transcript.
func Detect(t transcript.Transcript, opts Options) (Result, error) {
	if opts.TotalMS <= 0 {
		opts.TotalMS = t.DurationMS
	}
	if opts.TotalMS <= 0 {
		return Result{}, errors.New("the source duration is unknown, so there is nothing to cut against")
	}

	words := t.Words()
	if len(words) == 0 {
		return Result{}, ErrNoWordTimings
	}

	base := DefaultWords
	if opts.Aggressive {
		base = append(append([]string(nil), DefaultWords...), AggressiveWords...)
	}
	if len(opts.Only) > 0 {
		base = opts.Only
	}
	vocab := newVocabulary(base, opts.Extra, opts.Keep)

	cuts := detectPhrases(words, vocab)
	if opts.Repeats {
		cuts = append(cuts, detectRepeats(words, cuts)...)
	}
	if opts.MaxPause > 0 {
		cuts = append(cuts, detectPauses(words, opts)...)
	}

	// Shrink each cut away from its neighbours before inverting: word
	// boundaries from a recognizer are approximate, and a cut placed exactly on
	// one clips the consonant next to it.
	//
	// A cut narrower than the padding closes entirely, and one that will not be
	// applied has no business appearing in the report — it would read as an
	// edit that then failed to show up in the totals.
	spans := make(timespan.Ranges, 0, len(cuts))
	applied := make([]Cut, 0, len(cuts))
	for _, c := range cuts {
		shrunk := timespan.Ranges{c.Range}.Shrink(opts.PadHead, opts.PadTail)
		if len(shrunk) == 0 {
			continue
		}
		spans = append(spans, shrunk...)
		applied = append(applied, c)
	}
	spans = spans.Normalize().Clamp(opts.TotalMS)

	result := Result{
		Cuts:       applied,
		SourceMS:   opts.TotalMS,
		Counts:     map[string]int{},
		Vocabulary: vocab.Words(),
	}
	for _, c := range applied {
		result.Counts[string(c.Kind)]++
	}

	keep := spans.Invert(opts.TotalMS).DropShorterThan(opts.MinKeep.Milliseconds())
	result.Keep = keep
	result.OutputMS = keep.Total()
	result.RemovedMS = opts.TotalMS - result.OutputMS
	sortCuts(result.Cuts)
	return result, nil
}

// detectPhrases scans for filler phrases, preferring the longest match at each
// position so "you know" is one cut rather than a cut of "you" and a survivor.
func detectPhrases(words []transcript.Word, vocab vocabulary) []Cut {
	var out []Cut
	for i := 0; i < len(words); {
		matched := false
		limit := min(vocab.maxLen, len(words)-i)
		for n := limit; n >= 1; n-- {
			var joined strings.Builder
			for _, w := range words[i : i+n] {
				joined.WriteString(normalize(w.Text))
			}
			original, ok := vocab.lookup(joined.String())
			if !ok {
				continue
			}
			out = append(out, Cut{
				Range: timespan.Range{StartMS: words[i].StartMS, EndMS: words[i+n-1].EndMS},
				Kind:  KindFiller, Text: original,
			})
			i += n
			matched = true
			break
		}
		if !matched {
			i++
		}
	}
	return out
}

// repeatGapMS is how close two identical words must be to read as a stutter
// rather than as deliberate repetition. "很好，很好" said across a breath is
// emphasis; "很很好" is not.
const repeatGapMS = 500

// detectRepeats finds a word immediately repeated and cuts the first instance,
// keeping the second: the second attempt is the one the speaker finished.
func detectRepeats(words []transcript.Word, existing []Cut) []Cut {
	covered := coverage(existing)
	var out []Cut
	for i := 0; i+1 < len(words); i++ {
		a, b := words[i], words[i+1]
		if covered(a.StartMS) || covered(b.StartMS) {
			continue
		}
		na, nb := normalize(a.Text), normalize(b.Text)
		if na == "" || na != nb {
			continue
		}
		if b.StartMS-a.EndMS > repeatGapMS {
			continue
		}
		out = append(out, Cut{
			Range: timespan.Range{StartMS: a.StartMS, EndMS: b.StartMS},
			Kind:  KindRepeat, Text: strings.TrimSpace(a.Text),
		})
	}
	return out
}

// detectPauses shortens dead air to MaxPause, cutting from the middle so the
// tail of the previous phrase and the breath before the next one both survive.
func detectPauses(words []transcript.Word, opts Options) []Cut {
	limit := opts.MaxPause.Milliseconds()
	var out []Cut

	add := func(gap timespan.Range) {
		excess := gap.Duration() - limit
		if excess <= 0 {
			return
		}
		margin := limit / 2
		out = append(out, Cut{
			Range: timespan.Range{StartMS: gap.StartMS + margin, EndMS: gap.EndMS - (limit - margin)},
			Kind:  KindPause, Text: formatMS(gap.Duration()),
		})
	}

	if opts.TrimEnds && words[0].StartMS > 0 {
		add(timespan.Range{StartMS: 0, EndMS: words[0].StartMS})
	}
	for i := 0; i+1 < len(words); i++ {
		if gap := words[i+1].StartMS - words[i].EndMS; gap > limit {
			add(timespan.Range{StartMS: words[i].EndMS, EndMS: words[i+1].StartMS})
		}
	}
	if last := words[len(words)-1]; opts.TrimEnds && opts.TotalMS > last.EndMS {
		add(timespan.Range{StartMS: last.EndMS, EndMS: opts.TotalMS})
	}
	return out
}

// coverage returns a predicate reporting whether a timestamp is already inside
// a cut, so two rules never claim the same span.
func coverage(cuts []Cut) func(int64) bool {
	spans := make(timespan.Ranges, 0, len(cuts))
	for _, c := range cuts {
		spans = append(spans, c.Range)
	}
	spans = spans.Normalize()
	return func(ms int64) bool {
		for _, s := range spans {
			if s.Contains(ms) {
				return true
			}
		}
		return false
	}
}

func sortCuts(cuts []Cut) {
	for i := 1; i < len(cuts); i++ {
		for j := i; j > 0 && cuts[j].StartMS < cuts[j-1].StartMS; j-- {
			cuts[j], cuts[j-1] = cuts[j-1], cuts[j]
		}
	}
}

func formatMS(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(10 * time.Millisecond).String()
}
