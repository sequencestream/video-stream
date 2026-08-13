package audio

import (
	"context"
	"strings"
	"unicode"
)

// RawSynthesizer reports how long a line comes out before any time-stretching.
//
// This is a separate interface from TTS on purpose. TTS.Synthesize stretches
// its output into the seg's budget and refuses when that would exceed
// ±MaxStretchPercent, so it cannot answer "how long is this line?" — the
// question you have to answer before a budget exists.
type RawSynthesizer interface {
	RawDurationMS(ctx context.Context, text, voice string) (int64, error)
}

// TTSProbe measures narration duration by synthesizing the line for real.
//
// It is the honest way to get the number: the render pipeline cuts video to the
// budget midpoint while the audio track carries whatever the engine produced,
// so anything short of a real synthesis pass is a guess that shows up as
// truncated speech.
type TTSProbe struct {
	TTS RawSynthesizer
}

// ProbeMS synthesizes text once and reports its unstretched duration.
func (p TTSProbe) ProbeMS(ctx context.Context, text, voice string) (int64, error) {
	return p.TTS.RawDurationMS(ctx, strings.TrimSpace(text), voice)
}

// EstimateProbe derives duration from text length instead of synthesizing.
//
// This exists for the offline stub path, where no audio is produced at all. The
// numbers it returns are plausible, not real: a project imported through it
// renders with silent-adjacent stub audio, so the budgets never meet a waveform
// they have to agree with.
type EstimateProbe struct {
	// MSPerRune covers scripts without word spacing (CJK), where one rune is
	// roughly one syllable.
	MSPerRune int64
	// MSPerWord covers space-separated scripts.
	MSPerWord int64
}

// Default speaking rates: ~4.5 Mandarin syllables and ~3.3 English words per
// second, both at the unhurried end of narration pace.
const (
	defaultMSPerRune = 220
	defaultMSPerWord = 300
)

// ProbeMS estimates how long text takes to speak.
func (p EstimateProbe) ProbeMS(_ context.Context, text, _ string) (int64, error) {
	msPerRune, msPerWord := p.MSPerRune, p.MSPerWord
	if msPerRune <= 0 {
		msPerRune = defaultMSPerRune
	}
	if msPerWord <= 0 {
		msPerWord = defaultMSPerWord
	}

	// Mixed scripts are billed per rune for the wide part and per word for the
	// narrow part, so a Mandarin line carrying an English brand name is not
	// counted twice.
	var wide, narrowWords int64
	inNarrowWord := false
	for _, r := range text {
		switch {
		case r > unicode.MaxLatin1 && !unicode.IsPunct(r):
			wide++
			inNarrowWord = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if !inNarrowWord {
				narrowWords++
				inNarrowWord = true
			}
		default:
			inNarrowWord = false
		}
	}
	return wide*msPerRune + narrowWords*msPerWord, nil
}
