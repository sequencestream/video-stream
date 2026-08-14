package transcript

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Subtitle is one displayable caption: a time span and the lines to show.
//
// It is deliberately not a Cue. A recognizer's cue is "what was said between
// two pauses" and can run twenty seconds; a subtitle is "what fits on screen
// and can be read in the time it is up". Conflating them is why auto-generated
// captions so often show a wall of text for three seconds.
type Subtitle struct {
	Index   int      `json:"index"`
	StartMS int64    `json:"start_ms"`
	EndMS   int64    `json:"end_ms"`
	Lines   []string `json:"lines"`
}

// Text joins the subtitle's lines with newlines.
func (s Subtitle) Text() string { return strings.Join(s.Lines, "\n") }

// Duration is the caption's on-screen time in milliseconds.
func (s Subtitle) Duration() int64 { return s.EndMS - s.StartMS }

// LineOptions controls how a transcript is broken into captions.
type LineOptions struct {
	// MaxChars is the soft cap on characters per line.
	MaxChars int
	// MaxLines caps how many lines one caption may occupy.
	MaxLines int
	// MaxDurationMS forces a break once a caption has been up this long.
	MaxDurationMS int64
	// MinDurationMS extends a caption that would flash by unreadably, but
	// never past the next caption's start.
	MinDurationMS int64
	// GapMS is the silence between two words that ends a caption regardless of
	// length: a pause is where a reader expects the text to change.
	GapMS int64
}

// DefaultLineOptions returns sensible caption geometry for the given line
// width. Two lines is the ceiling almost every platform's safe area allows.
func DefaultLineOptions(maxChars, maxLines int) LineOptions {
	if maxChars <= 0 {
		maxChars = 20
	}
	if maxLines <= 0 {
		maxLines = 2
	}
	return LineOptions{
		MaxChars: maxChars, MaxLines: maxLines,
		MaxDurationMS: 6000, MinDurationMS: 800, GapMS: 700,
	}
}

// Subtitles breaks the transcript into captions.
//
// With word timings each caption gets the exact span of the words it shows.
// Without them the cue's span is divided among its captions in proportion to
// their length, which is the honest approximation: no word timing means no
// better answer exists.
func (t Transcript) Subtitles(opts LineOptions) []Subtitle {
	opts = opts.withDefaults()

	var out []Subtitle
	for _, cue := range t.Cues {
		if len(cue.Words) > 0 {
			out = append(out, subtitlesFromWords(cue, opts)...)
			continue
		}
		out = append(out, subtitlesFromText(cue, opts)...)
	}

	enforceMinDuration(out, opts.MinDurationMS)
	for i := range out {
		out[i].Index = i + 1
	}
	return out
}

func (o LineOptions) withDefaults() LineOptions {
	d := DefaultLineOptions(o.MaxChars, o.MaxLines)
	if o.MaxDurationMS > 0 {
		d.MaxDurationMS = o.MaxDurationMS
	}
	if o.MinDurationMS > 0 {
		d.MinDurationMS = o.MinDurationMS
	}
	if o.GapMS > 0 {
		d.GapMS = o.GapMS
	}
	return d
}

// subtitlesFromWords packs words into captions, breaking at the first of:
// a sentence-final punctuation mark, a silence longer than GapMS, the
// character budget, or MaxDurationMS.
func subtitlesFromWords(cue Cue, opts LineOptions) []Subtitle {
	budget := opts.MaxChars * opts.MaxLines

	var (
		out   []Subtitle
		batch []Word
		text  string
	)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		out = append(out, Subtitle{
			StartMS: batch[0].StartMS,
			EndMS:   batch[len(batch)-1].EndMS,
			Lines:   WrapLines(text, opts.MaxChars, opts.MaxLines),
		})
		batch, text = nil, ""
	}

	for _, w := range cue.Words {
		if len(batch) > 0 {
			prev := batch[len(batch)-1]
			switch {
			case w.StartMS-prev.EndMS >= opts.GapMS:
				flush()
			case endsSentence(prev.Text):
				flush()
			case runeLen(JoinText([]string{text, w.Text})) > budget:
				flush()
			case w.EndMS-batch[0].StartMS > opts.MaxDurationMS:
				flush()
			}
		}
		batch = append(batch, w)
		text = JoinText([]string{text, w.Text})
	}
	flush()
	return out
}

// subtitlesFromText splits a cue with no word timing, dividing its span among
// the resulting captions in proportion to their character count.
func subtitlesFromText(cue Cue, opts LineOptions) []Subtitle {
	text := strings.TrimSpace(cue.Text)
	if text == "" {
		return nil
	}
	chunks := splitToBudget(text, opts.MaxChars*opts.MaxLines)
	if len(chunks) == 0 {
		return nil
	}

	total := 0
	for _, c := range chunks {
		total += runeLen(c)
	}
	if total == 0 {
		return nil
	}

	span := cue.Duration()
	out := make([]Subtitle, 0, len(chunks))
	cursor := cue.StartMS
	consumed := 0
	for i, c := range chunks {
		consumed += runeLen(c)
		end := cue.StartMS + span*int64(consumed)/int64(total)
		if i == len(chunks)-1 {
			end = cue.EndMS
		}
		if end < cursor {
			end = cursor
		}
		out = append(out, Subtitle{
			StartMS: cursor, EndMS: end,
			Lines: WrapLines(c, opts.MaxChars, opts.MaxLines),
		})
		cursor = end
	}
	return out
}

// enforceMinDuration stretches captions that would flash past unreadably,
// stopping at the next caption's start so nothing is ever shown twice.
func enforceMinDuration(subs []Subtitle, minMS int64) {
	if minMS <= 0 {
		return
	}
	for i := range subs {
		if subs[i].Duration() >= minMS {
			continue
		}
		want := subs[i].StartMS + minMS
		if i+1 < len(subs) && want > subs[i+1].StartMS {
			want = subs[i+1].StartMS
		}
		if want > subs[i].EndMS {
			subs[i].EndMS = want
		}
	}
}

// WrapLines breaks text into at most maxLines lines of about maxChars each.
//
// Lines are balanced rather than greedily filled: a two-line caption reading
// 19 + 3 characters looks broken, and 11 + 11 does not. When the text simply
// does not fit, the width is relaxed rather than the text truncated — dropping
// a speaker's words to respect a layout preference is never the right trade.
func WrapLines(text string, maxChars, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxChars <= 0 {
		return []string{text}
	}
	if maxLines <= 0 {
		maxLines = 1
	}

	total := runeLen(text)
	lines := min(max((total+maxChars-1)/maxChars, 1), maxLines)
	// Balance: aim for equal lines rather than filling the first to the brim.
	width := max((total+lines-1)/lines, 1)

	out := wrapGreedy(text, width)
	// Word boundaries can push the greedy pass one line over; widen until it
	// fits rather than cutting text.
	for len(out) > maxLines && width < total {
		width = width + (width+3)/4 + 1
		out = wrapGreedy(text, width)
	}
	return out
}

// splitToBudget chops text into chunks no longer than budget characters,
// preferring to break after punctuation.
func splitToBudget(text string, budget int) []string {
	if budget <= 0 || runeLen(text) <= budget {
		return []string{text}
	}
	var out []string
	runes := []rune(text)
	for len(runes) > budget {
		cut := budget
		// Walk back to the last punctuation or space in the final third, so a
		// break lands where the sentence already pauses.
		for i := budget; i > budget*2/3; i-- {
			if isBreakable(runes[i-1]) {
				cut = i
				break
			}
		}
		chunk := strings.TrimSpace(string(runes[:cut]))
		if chunk != "" {
			out = append(out, chunk)
		}
		runes = runes[cut:]
	}
	if rest := strings.TrimSpace(string(runes)); rest != "" {
		out = append(out, rest)
	}
	return out
}

// wrapGreedy fills lines to width, breaking between Latin words and between
// any two characters in scripts written without spaces.
func wrapGreedy(text string, width int) []string {
	units := splitUnits(text)
	var (
		out []string
		cur strings.Builder
		n   int
	)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
		n = 0
	}
	for _, u := range units {
		ul := runeLen(u)
		if n > 0 && n+ul > width {
			flush()
		}
		if n > 0 && needsSpace(lastRune(cur.String()), firstRune(u)) {
			cur.WriteByte(' ')
			n++
		}
		cur.WriteString(u)
		n += ul
	}
	flush()
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

// splitUnits breaks text into the smallest pieces a line may break between:
// whole words for space-separated scripts, single characters for CJK.
func splitUnits(text string) []string {
	var (
		out  []string
		word strings.Builder
	)
	flushWord := func() {
		if word.Len() > 0 {
			out = append(out, word.String())
			word.Reset()
		}
	}
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			flushWord()
		case isCJK(r):
			flushWord()
			out = append(out, string(r))
		default:
			word.WriteRune(r)
		}
	}
	flushWord()
	return out
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// endsSentence reports whether a token closes a sentence, which is the most
// natural place to change what is on screen.
func endsSentence(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	switch lastRune(s) {
	case '.', '!', '?', '。', '！', '？', '…', ';', '；':
		return true
	default:
		return false
	}
}

func isBreakable(r rune) bool {
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case ',', '.', '!', '?', ';', ':',
		'，', '。', '！', '？', '；', '：', '、', '…':
		return true
	default:
		return false
	}
}
