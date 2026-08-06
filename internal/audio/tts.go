package audio

import (
	"context"
	"fmt"
	"strings"

	"github.com/sequencestream/video-stream/internal/model"
)

// SegResult is TTS output for one seg.
type SegResult struct {
	SegID      string
	ActualMS   int64
	Rate       float64
	Tokens     []model.Token
	AudioURI   string
}

// TTS synthesizes speech for one seg.
type TTS interface {
	Synthesize(ctx context.Context, seg model.Seg, voice string) (SegResult, error)
}

// StubTTS produces deterministic word timings from text length.
type StubTTS struct {
	MSPerWord int64
}

func (s StubTTS) Synthesize(_ context.Context, seg model.Seg, _ string) (SegResult, error) {
	if s.MSPerWord <= 0 {
		s.MSPerWord = 180
	}
	words := strings.Fields(seg.Text)
	if len(words) == 0 {
		words = []string{"..."}
	}
	actual := s.MSPerWord * int64(len(words))
	rate, err := PlaybackRate(actual, seg.DurationBudget)
	if err != nil {
		return SegResult{}, err
	}
	actual = AdjustedDurationMS(actual, rate)
	tokens := allocateTokens(seg.SegID, words, actual)
	return SegResult{
		SegID: seg.SegID, ActualMS: actual, Rate: rate, Tokens: tokens,
		AudioURI: "stub-audio://" + seg.SegID,
	}, nil
}

// LongStubTTS always exceeds budget for tests.
type LongStubTTS struct{}

func (LongStubTTS) Synthesize(_ context.Context, seg model.Seg, _ string) (SegResult, error) {
	actual := seg.DurationBudget.MaxMS * 2
	_, err := PlaybackRate(actual, seg.DurationBudget)
	return SegResult{SegID: seg.SegID, ActualMS: actual}, err
}

func allocateTokens(segID string, words []string, totalMS int64) []model.Token {
	if len(words) == 0 {
		return nil
	}
	per := totalMS / int64(len(words))
	tokens := make([]model.Token, len(words))
	var at int64
	for i, w := range words {
		end := at + per
		if i == len(words)-1 {
			end = totalMS
		}
		tokens[i] = model.Token{
			ID: fmt.Sprintf("tok-%s-%d", segID, i), Text: w,
			StartMS: at, EndMS: end,
			Source: model.SourceTTSAlign, EditState: model.EditKept, Confidence: 1,
		}
		at = end
	}
	return tokens
}
