package model

import "fmt"

// TokenSource records how a token's timing was obtained.
type TokenSource string

// Token sources. The MVP only ever produces SourceTTSAlign; the other two exist
// so that ASR-derived and hand-corrected timings can coexist in one timeline
// without a schema change, and so that a downstream consumer can tell how much
// to trust a boundary.
const (
	SourceTTSAlign TokenSource = "tts_align"
	SourceASR      TokenSource = "asr"
	SourceManual   TokenSource = "manual"
)

// Valid reports whether s is a known source.
func (s TokenSource) Valid() bool {
	switch s {
	case SourceTTSAlign, SourceASR, SourceManual:
		return true
	default:
		return false
	}
}

// EditState is the editorial verdict on a token.
type EditState string

// Edit states. These implement "tag, never delete": a token marked EditDropped
// keeps its text and its timestamps, and only the renderer skips it, so any
// edit can be undone without re-running alignment.
//
// The MVP evaluates none of them. Under a pure TTS pipeline there are no
// fillers and no dead air to find, so a tagger would have nothing to read;
// only the field and its permitted values are delivered here.
const (
	EditKept    EditState = "kept"
	EditFiller  EditState = "filler"
	EditSilence EditState = "silence"
	EditRepeat  EditState = "repeat"
	EditDropped EditState = "dropped"
)

// Valid reports whether e is a known edit state.
func (e EditState) Valid() bool {
	switch e {
	case EditKept, EditFiller, EditSilence, EditRepeat, EditDropped:
		return true
	default:
		return false
	}
}

// EventKind classifies a top-level timeline chunk.
type EventKind string

// Event kinds.
const (
	EventSpeech EventKind = "speech"
	EventScene  EventKind = "scene"
)

// Valid reports whether k is a known event kind.
func (k EventKind) Valid() bool {
	return k == EventSpeech || k == EventScene
}

// Token is one word with its timing. It is the bottom layer of the timeline and
// the unit subtitle segmentation works on.
type Token struct {
	ID         string      `json:"id"`
	Text       string      `json:"text"`
	StartMS    int64       `json:"start_ms"`
	EndMS      int64       `json:"end_ms"`
	Confidence float64     `json:"confidence"`
	Speaker    string      `json:"speaker,omitempty"`
	Source     TokenSource `json:"source"`
	EditState  EditState   `json:"edit_state"`
}

// Utterance groups the tokens produced from one seg.
type Utterance struct {
	ID      string  `json:"id"`
	SegID   string  `json:"seg_id"`
	Speaker string  `json:"speaker,omitempty"`
	Tokens  []Token `json:"tokens"`
}

// Event groups utterances into one semantically meaningful chunk.
type Event struct {
	ID         string      `json:"id"`
	Kind       EventKind   `json:"kind"`
	Utterances []Utterance `json:"utterances"`
}

// Timeline is the word-level alignment of a whole project.
//
// In the MVP it is produced by TTS alignment and consumed by subtitle
// segmentation, which walks event to utterance to token.
type Timeline struct {
	Events []Event `json:"events"`
}

// SpanMS returns the half-open time span an utterance covers.
func (u Utterance) SpanMS() (int64, int64) {
	if len(u.Tokens) == 0 {
		return 0, 0
	}
	return u.Tokens[0].StartMS, u.Tokens[len(u.Tokens)-1].EndMS
}

// SpanMS returns the time span an event covers.
func (e Event) SpanMS() (int64, int64) {
	if len(e.Utterances) == 0 {
		return 0, 0
	}
	start, _ := e.Utterances[0].SpanMS()
	_, end := e.Utterances[len(e.Utterances)-1].SpanMS()
	return start, end
}

// DurationMS is the end of the last event.
func (t Timeline) DurationMS() int64 {
	if len(t.Events) == 0 {
		return 0
	}
	_, end := t.Events[len(t.Events)-1].SpanMS()
	return end
}

// Tokens flattens the timeline in playback order.
func (t Timeline) Tokens() []Token {
	var out []Token
	for _, e := range t.Events {
		for _, u := range e.Utterances {
			out = append(out, u.Tokens...)
		}
	}
	return out
}

// Validate enforces the timeline invariants.
//
// Ordering and non-overlap are hard requirements rather than warnings: subtitle
// segmentation that meets two overlapping spans emits two subtitles on screen
// at once, which is a defect the viewer sees directly.
func (t Timeline) Validate() error {
	ids := make(map[string]struct{})
	claim := func(kind, id string) error {
		if id == "" {
			return fmt.Errorf("%s id must not be empty", kind)
		}
		if _, dup := ids[id]; dup {
			return fmt.Errorf("timeline id %s is used more than once", id)
		}
		ids[id] = struct{}{}
		return nil
	}

	prevEventEnd := int64(-1)
	for ei, e := range t.Events {
		if err := claim("event", e.ID); err != nil {
			return err
		}
		if !e.Kind.Valid() {
			return fmt.Errorf("event %s: kind %q is not a known kind", e.ID, e.Kind)
		}
		if len(e.Utterances) == 0 {
			return fmt.Errorf("event %s: must hold at least one utterance", e.ID)
		}

		if err := validateUtterances(e, ids, claim); err != nil {
			return err
		}

		start, end := e.SpanMS()
		if start < prevEventEnd {
			return fmt.Errorf("event %s starts at %dms, before event[%d] ended at %dms", e.ID, start, ei-1, prevEventEnd)
		}
		prevEventEnd = end
	}
	return nil
}

func validateUtterances(e Event, ids map[string]struct{}, claim func(kind, id string) error) error {
	prevEnd := int64(-1)
	for ui, u := range e.Utterances {
		if err := claim("utterance", u.ID); err != nil {
			return err
		}
		if u.SegID == "" {
			return fmt.Errorf("utterance %s: seg_id must not be empty", u.ID)
		}
		if len(u.Tokens) == 0 {
			return fmt.Errorf("utterance %s: must hold at least one token", u.ID)
		}

		tokenEnd := int64(-1)
		for ti, tok := range u.Tokens {
			if err := claim("token", tok.ID); err != nil {
				return err
			}
			if tok.Text == "" {
				return fmt.Errorf("token %s: text must not be empty", tok.ID)
			}
			if tok.StartMS < 0 {
				return fmt.Errorf("token %s: start_ms must not be negative, got %d", tok.ID, tok.StartMS)
			}
			if tok.EndMS <= tok.StartMS {
				return fmt.Errorf("token %s: end_ms %d must be after start_ms %d", tok.ID, tok.EndMS, tok.StartMS)
			}
			if tok.Confidence < 0 || tok.Confidence > 1 {
				return fmt.Errorf("token %s: confidence %v is outside [0,1]", tok.ID, tok.Confidence)
			}
			if !tok.Source.Valid() {
				return fmt.Errorf("token %s: source %q is not a known source", tok.ID, tok.Source)
			}
			if !tok.EditState.Valid() {
				return fmt.Errorf("token %s: edit_state %q is not a known state", tok.ID, tok.EditState)
			}
			if tok.StartMS < tokenEnd {
				return fmt.Errorf("token %s overlaps token[%d] which ended at %dms", tok.ID, ti-1, tokenEnd)
			}
			tokenEnd = tok.EndMS
		}

		start, end := u.SpanMS()
		if start < prevEnd {
			return fmt.Errorf("utterance %s starts at %dms, before utterance[%d] ended at %dms", u.ID, start, ui-1, prevEnd)
		}
		prevEnd = end
	}
	return nil
}

// Empty reports whether the timeline carries no alignment at all.
func (t Timeline) Empty() bool { return len(t.Events) == 0 }
