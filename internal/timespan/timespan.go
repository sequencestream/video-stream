// Package timespan is the vocabulary every vs command shares for talking about
// time: an interval, a list of intervals, and the conversions between the
// timestamp forms a person types and the ones ffmpeg reads.
//
// It exists as its own package because both halves of the tool need it and
// neither should depend on the other. The recognizer produces spans, the
// cutter consumes them, and the flags parse them.
package timespan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Range is a half-open interval on the media timeline, in milliseconds.
//
// Both the cutting commands and the recognizer speak in these, which is what
// lets `vs filler` hand its keep list straight to the trimmer without a
// translation layer that could disagree about whether an end is inclusive.
type Range struct {
	StartMS int64 `json:"start_ms"`
	EndMS   int64 `json:"end_ms"`
}

// Duration is the range's length in milliseconds.
func (r Range) Duration() int64 { return r.EndMS - r.StartMS }

// Empty reports whether the range carries no time.
func (r Range) Empty() bool { return r.EndMS <= r.StartMS }

// Contains reports whether ms falls inside the range.
func (r Range) Contains(ms int64) bool { return ms >= r.StartMS && ms < r.EndMS }

// Overlaps reports whether the two ranges share any time.
func (r Range) Overlaps(o Range) bool { return r.StartMS < o.EndMS && o.StartMS < r.EndMS }

// String renders the range as the same hh:mm:ss.mmm form the flags accept.
func (r Range) String() string { return FormatTime(r.StartMS) + "-" + FormatTime(r.EndMS) }

// Ranges is an ordered list of intervals.
type Ranges []Range

// Total is the summed duration of every range.
func (rs Ranges) Total() int64 {
	var sum int64
	for _, r := range rs {
		if !r.Empty() {
			sum += r.Duration()
		}
	}
	return sum
}

// Normalize sorts, drops empty ranges, and merges any that touch or overlap.
//
// Everything downstream assumes a normalized list. Two overlapping keep ranges
// would otherwise duplicate a slice of video into the output, and the caller
// building them — a filler pass padding each side of every gap — produces
// overlaps routinely.
func (rs Ranges) Normalize() Ranges {
	if len(rs) == 0 {
		return nil
	}
	sorted := make(Ranges, 0, len(rs))
	for _, r := range rs {
		if !r.Empty() {
			sorted = append(sorted, r)
		}
	}
	if len(sorted) == 0 {
		return nil
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].StartMS != sorted[j].StartMS {
			return sorted[i].StartMS < sorted[j].StartMS
		}
		return sorted[i].EndMS < sorted[j].EndMS
	})

	out := Ranges{sorted[0]}
	for _, r := range sorted[1:] {
		last := &out[len(out)-1]
		if r.StartMS <= last.EndMS {
			if r.EndMS > last.EndMS {
				last.EndMS = r.EndMS
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// Clamp restricts every range to [0, totalMS] and re-normalizes.
func (rs Ranges) Clamp(totalMS int64) Ranges {
	out := make(Ranges, 0, len(rs))
	for _, r := range rs {
		if r.StartMS < 0 {
			r.StartMS = 0
		}
		if totalMS > 0 && r.EndMS > totalMS {
			r.EndMS = totalMS
		}
		if !r.Empty() {
			out = append(out, r)
		}
	}
	return out.Normalize()
}

// Invert returns the complement of rs within [0, totalMS].
//
// This is how a "cut these out" list becomes the "keep these" list the trimmer
// wants, and vice versa. Both directions are needed: a user thinks in terms of
// what to remove, ffmpeg is told what to keep.
func (rs Ranges) Invert(totalMS int64) Ranges {
	normalized := rs.Clamp(totalMS)
	var out Ranges
	cursor := int64(0)
	for _, r := range normalized {
		if r.StartMS > cursor {
			out = append(out, Range{StartMS: cursor, EndMS: r.StartMS})
		}
		if r.EndMS > cursor {
			cursor = r.EndMS
		}
	}
	if cursor < totalMS {
		out = append(out, Range{StartMS: cursor, EndMS: totalMS})
	}
	return out
}

// Pad widens every range by head and tail, then re-normalizes. Padding is what
// keeps a cut from clipping the attack of the word that follows it.
func (rs Ranges) Pad(head, tail time.Duration, totalMS int64) Ranges {
	out := make(Ranges, 0, len(rs))
	for _, r := range rs {
		r.StartMS -= head.Milliseconds()
		r.EndMS += tail.Milliseconds()
		out = append(out, r)
	}
	return out.Clamp(totalMS)
}

// Shrink narrows every range by head at the start and tail at the end, dropping
// any that close entirely.
//
// It is Pad's inverse and the one a cut list wants: pulling a cut away from the
// speech on either side is how a removal stops clipping the consonant next to
// it, since a recognizer's word boundaries are approximate.
func (rs Ranges) Shrink(head, tail time.Duration) Ranges {
	out := make(Ranges, 0, len(rs))
	for _, r := range rs {
		r.StartMS += head.Milliseconds()
		r.EndMS -= tail.Milliseconds()
		if !r.Empty() {
			out = append(out, r)
		}
	}
	return out.Normalize()
}

// DropShorterThan removes ranges below minMS.
//
// Applied to a keep list this deletes speech islands too short to read as
// words; applied to a cut list it leaves micro-gaps alone, because splicing
// out 30 ms costs a re-encode and buys nothing audible.
func (rs Ranges) DropShorterThan(minMS int64) Ranges {
	if minMS <= 0 {
		return rs
	}
	out := make(Ranges, 0, len(rs))
	for _, r := range rs {
		if r.Duration() >= minMS {
			out = append(out, r)
		}
	}
	return out
}

// MapTime projects a source timestamp onto the timeline produced by keeping
// only rs, returning false for a timestamp that lands inside a removed span.
//
// Subtitles are the reason this exists: after a filler cut the words that
// survive must carry their new times, or every cue after the first cut is late
// by the amount removed before it.
func (rs Ranges) MapTime(ms int64) (int64, bool) {
	var elapsed int64
	for _, r := range rs {
		if ms < r.StartMS {
			return elapsed, false
		}
		if ms < r.EndMS {
			return elapsed + (ms - r.StartMS), true
		}
		elapsed += r.Duration()
	}
	return elapsed, false
}

// ParseRange parses "start-end", where either side may be empty to mean the
// start or the end of the media. Both sides accept the forms ParseTime does.
func ParseRange(s string, totalMS int64) (Range, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Range{}, fmt.Errorf("empty range")
	}
	// Split on the first '-', so "1:00-2:00" works and a lone "-30" is read as
	// "from the start to 30s".
	before, after, found := strings.Cut(raw, "-")
	if !found {
		return Range{}, fmt.Errorf("range %q must be START-END", s)
	}
	startText, endText := strings.TrimSpace(before), strings.TrimSpace(after)

	var r Range
	var err error
	if startText == "" {
		r.StartMS = 0
	} else if r.StartMS, err = ParseTime(startText); err != nil {
		return Range{}, fmt.Errorf("range %q: %w", s, err)
	}
	if endText == "" {
		if totalMS <= 0 {
			return Range{}, fmt.Errorf("range %q has an open end but the media duration is unknown", s)
		}
		r.EndMS = totalMS
	} else if r.EndMS, err = ParseTime(endText); err != nil {
		return Range{}, fmt.Errorf("range %q: %w", s, err)
	}
	if r.EndMS < r.StartMS {
		return Range{}, fmt.Errorf("range %q ends before it starts", s)
	}
	return r, nil
}

// ParseTime reads a timestamp in any of the forms a person actually types:
// "12.5" seconds, "1:05", "01:02:03.250", or a Go duration like "90s".
func ParseTime(s string) (int64, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("empty timestamp")
	}
	if strings.ContainsAny(raw, "hmsu") && !strings.Contains(raw, ":") {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("timestamp %q: %w", s, err)
		}
		return d.Milliseconds(), nil
	}

	parts := strings.Split(raw, ":")
	if len(parts) > 3 {
		return 0, fmt.Errorf("timestamp %q has too many colon-separated fields", s)
	}
	var total float64
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0, fmt.Errorf("timestamp %q: %w", s, err)
		}
		if v < 0 {
			return 0, fmt.Errorf("timestamp %q must not be negative", s)
		}
		total = total*60 + v
	}
	return int64(total*1000 + 0.5), nil
}

// FormatTime renders milliseconds as hh:mm:ss.mmm.
func FormatTime(ms int64) string {
	neg := ""
	if ms < 0 {
		neg, ms = "-", -ms
	}
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%s%02d:%02d:%02d.%03d", neg, h, m, s, ms)
}

// FormatSeconds renders milliseconds as the plain seconds ffmpeg's -ss and -t
// options take. Colon forms are also accepted by ffmpeg, but seconds survive
// arithmetic without a parse step in between.
func FormatSeconds(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64)
}
