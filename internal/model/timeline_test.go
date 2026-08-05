package model_test

import (
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
)

func newToken(id, text string, startMS, endMS int64) model.Token {
	return model.Token{
		ID:         id,
		Text:       text,
		StartMS:    startMS,
		EndMS:      endMS,
		Confidence: 1,
		Source:     model.SourceTTSAlign,
		EditState:  model.EditKept,
	}
}

func newTimeline(tokens ...model.Token) model.Timeline {
	return model.Timeline{Events: []model.Event{{
		ID:         "e1",
		Kind:       model.EventSpeech,
		Utterances: []model.Utterance{{ID: "u1", SegID: "a", Tokens: tokens}},
	}}}
}

func TestTimelineAcceptsAWellFormedAlignment(t *testing.T) {
	tl := newTimeline(
		newToken("t1", "增量", 0, 400),
		newToken("t2", "重编译", 400, 900),
	)
	if err := tl.Validate(); err != nil {
		t.Fatalf("a well-formed timeline was rejected: %v", err)
	}
	if got := tl.DurationMS(); got != 900 {
		t.Fatalf("DurationMS = %d, want 900", got)
	}
	if got := len(tl.Tokens()); got != 2 {
		t.Fatalf("Tokens returned %d entries, want 2", got)
	}
	if tl.Empty() {
		t.Fatal("a timeline with events must not report itself empty")
	}
}

// Overlapping spans are a defect the viewer sees directly: subtitle
// segmentation would put two lines on screen at once.
func TestTimelineRejectsOverlappingTokens(t *testing.T) {
	tl := newTimeline(
		newToken("t1", "增量", 0, 500),
		newToken("t2", "重编译", 400, 900),
	)
	err := tl.Validate()
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("got %v, want an overlap error", err)
	}
}

func TestTimelineRejectsMalformedTokens(t *testing.T) {
	runs := []struct {
		name  string
		token model.Token
		want  string
	}{
		{"empty id", model.Token{Text: "x", EndMS: 1, Source: model.SourceTTSAlign, EditState: model.EditKept}, "id must not be empty"},
		{"empty text", model.Token{ID: "t1", EndMS: 1, Source: model.SourceTTSAlign, EditState: model.EditKept}, "text"},
		{"zero length", model.Token{ID: "t1", Text: "x", StartMS: 5, EndMS: 5, Source: model.SourceTTSAlign, EditState: model.EditKept}, "end_ms"},
		{"negative start", model.Token{ID: "t1", Text: "x", StartMS: -1, EndMS: 5, Source: model.SourceTTSAlign, EditState: model.EditKept}, "start_ms"},
		{"confidence above one", model.Token{ID: "t1", Text: "x", EndMS: 5, Confidence: 1.5, Source: model.SourceTTSAlign, EditState: model.EditKept}, "confidence"},
		{"confidence below zero", model.Token{ID: "t1", Text: "x", EndMS: 5, Confidence: -0.1, Source: model.SourceTTSAlign, EditState: model.EditKept}, "confidence"},
		{"unknown source", model.Token{ID: "t1", Text: "x", EndMS: 5, Source: "telepathy", EditState: model.EditKept}, "source"},
		{"empty source", model.Token{ID: "t1", Text: "x", EndMS: 5, EditState: model.EditKept}, "source"},
		{"unknown edit state", model.Token{ID: "t1", Text: "x", EndMS: 5, Source: model.SourceTTSAlign, EditState: "erased"}, "edit_state"},
		{"empty edit state", model.Token{ID: "t1", Text: "x", EndMS: 5, Source: model.SourceTTSAlign}, "edit_state"},
	}
	for _, run := range runs {
		t.Run(run.name, func(t *testing.T) {
			err := newTimeline(run.token).Validate()
			if err == nil {
				t.Fatalf("%s should have been rejected", run.name)
			}
			if !strings.Contains(err.Error(), run.want) {
				t.Fatalf("error %q does not mention %q", err, run.want)
			}
		})
	}
}

// Reserved edit states must survive validation even though nothing in the MVP
// produces or reads them; rejecting them would make the V2 path a schema change.
func TestTimelineAcceptsReservedEditStates(t *testing.T) {
	for _, state := range []model.EditState{model.EditFiller, model.EditSilence, model.EditRepeat, model.EditDropped} {
		token := newToken("t1", "uh", 0, 100)
		token.EditState = state
		if err := newTimeline(token).Validate(); err != nil {
			t.Fatalf("edit_state %q was rejected: %v", state, err)
		}
	}
}

func TestTimelineRejectsDuplicateIDs(t *testing.T) {
	tl := newTimeline(
		newToken("t1", "增量", 0, 400),
		newToken("t1", "重编译", 400, 900),
	)
	err := tl.Validate()
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("got %v, want a duplicate id error", err)
	}
}

func TestTimelineRejectsEmptyLayers(t *testing.T) {
	noUtterances := model.Timeline{Events: []model.Event{{ID: "e1", Kind: model.EventSpeech}}}
	if err := noUtterances.Validate(); err == nil {
		t.Fatal("an event with no utterances must be rejected")
	}

	noTokens := model.Timeline{Events: []model.Event{{
		ID:         "e1",
		Kind:       model.EventSpeech,
		Utterances: []model.Utterance{{ID: "u1", SegID: "a"}},
	}}}
	if err := noTokens.Validate(); err == nil {
		t.Fatal("an utterance with no tokens must be rejected")
	}

	noSegID := model.Timeline{Events: []model.Event{{
		ID:   "e1",
		Kind: model.EventSpeech,
		Utterances: []model.Utterance{{
			ID:     "u1",
			Tokens: []model.Token{newToken("t1", "x", 0, 1)},
		}},
	}}}
	if err := noSegID.Validate(); err == nil {
		t.Fatal("an utterance with no seg_id must be rejected")
	}
}

func TestTimelineRejectsOutOfOrderUtterancesAndEvents(t *testing.T) {
	outOfOrderUtterances := model.Timeline{Events: []model.Event{{
		ID:   "e1",
		Kind: model.EventSpeech,
		Utterances: []model.Utterance{
			{ID: "u1", SegID: "a", Tokens: []model.Token{newToken("t1", "x", 500, 900)}},
			{ID: "u2", SegID: "b", Tokens: []model.Token{newToken("t2", "y", 100, 400)}},
		},
	}}}
	if err := outOfOrderUtterances.Validate(); err == nil {
		t.Fatal("utterances that move backwards in time must be rejected")
	}

	outOfOrderEvents := model.Timeline{Events: []model.Event{
		{ID: "e1", Kind: model.EventSpeech, Utterances: []model.Utterance{
			{ID: "u1", SegID: "a", Tokens: []model.Token{newToken("t1", "x", 500, 900)}},
		}},
		{ID: "e2", Kind: model.EventSpeech, Utterances: []model.Utterance{
			{ID: "u2", SegID: "b", Tokens: []model.Token{newToken("t2", "y", 100, 400)}},
		}},
	}}
	if err := outOfOrderEvents.Validate(); err == nil {
		t.Fatal("events that move backwards in time must be rejected")
	}
}

func TestTimelineRejectsUnknownEventKind(t *testing.T) {
	tl := newTimeline(newToken("t1", "x", 0, 100))
	tl.Events[0].Kind = "interlude"
	err := tl.Validate()
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("got %v, want an unknown-kind error", err)
	}
}

func TestEmptyTimelineIsValid(t *testing.T) {
	var tl model.Timeline
	if err := tl.Validate(); err != nil {
		t.Fatalf("an unaligned project must be valid: %v", err)
	}
	if !tl.Empty() {
		t.Fatal("a timeline with no events must report itself empty")
	}
	if got := tl.DurationMS(); got != 0 {
		t.Fatalf("DurationMS = %d, want 0", got)
	}
}

func TestBreathPauseLevels(t *testing.T) {
	if model.BreathNone.PauseMS() != 0 {
		t.Fatal("none must not pause")
	}
	if model.BreathShort.PauseMS() >= model.BreathLong.PauseMS() {
		t.Fatal("a short breath must be shorter than a long one")
	}
}
