// Package scriptagents runs the multi-agent script polishing loop.
//
// The bet is that quality comes from structural constraints — diagnose without
// prescribing, hybridise at the feature level, terminate early — not from
// stacking more LLM critics that praise each other into mediocrity.
package scriptagents

import "errors"

const (
	// WriterCount is how many parallel drafts the Writer produces.
	WriterCount = 3
	// DropOffSeconds are the moments Audience-Simulator reports against.
	DropOffSecondHook    = 3
	DropOffSecondContext = 8
	DropOffSecondPayoff  = 15
)

var (
	// ErrCriticRewrote means the critic output contained replacement text.
	ErrCriticRewrote = errors.New("critic output must not rewrite content")
	// ErrAudienceJudged means the audience report contained good/bad judgement.
	ErrAudienceJudged = errors.New("audience report must not judge quality")
	// ErrSpikeLost means the mandatory spike point was removed during polish.
	ErrSpikeLost = errors.New("spike point was removed during polish")
)
