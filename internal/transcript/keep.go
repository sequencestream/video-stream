package transcript

import "github.com/sequencestream/video-stream/internal/timespan"

// Keep returns a copy of the transcript restricted to the kept ranges, with
// every timestamp rebased onto the resulting timeline.
//
// Words are kept or dropped whole: a word straddling a cut boundary belongs to
// whichever side holds most of it, because half a word's timing on screen is
// worse than a 40 ms error.
func (t Transcript) Keep(keep timespan.Ranges) Transcript {
	keep = keep.Normalize()
	out := t
	out.Cues = nil
	out.DurationMS = keep.Total()

	for _, cue := range t.Cues {
		var (
			words   []Word
			started bool
			newCue  Cue
		)
		for _, w := range cue.Words {
			mid := w.StartMS + w.Duration()/2
			if _, ok := keep.MapTime(mid); !ok {
				continue
			}
			start, _ := keep.MapTime(w.StartMS)
			end, _ := keep.MapTime(w.EndMS)
			if end < start {
				end = start
			}
			w.StartMS, w.EndMS = start, end
			words = append(words, w)
			if !started {
				newCue.StartMS, started = start, true
			}
			newCue.EndMS = end
		}
		if len(cue.Words) == 0 {
			// No word timing to filter by: keep the cue whole if its midpoint
			// survived, which is the best a cue-level transcript supports.
			mid := cue.StartMS + cue.Duration()/2
			if _, ok := keep.MapTime(mid); !ok {
				continue
			}
			start, _ := keep.MapTime(cue.StartMS)
			end, _ := keep.MapTime(cue.EndMS)
			out.Cues = append(out.Cues, Cue{StartMS: start, EndMS: max64(start, end), Text: cue.Text})
			continue
		}
		if len(words) == 0 {
			continue
		}
		texts := make([]string, 0, len(words))
		for _, w := range words {
			texts = append(texts, w.Text)
		}
		newCue.Text = JoinText(texts)
		newCue.Words = words
		out.Cues = append(out.Cues, newCue)
	}
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
