// Package model is the video engineering core data model: the seg graph that
// describes what gets said, and the token/utterance/event timeline that
// describes when each word lands.
//
// Everything downstream — TTS, subtitle segmentation, rendering, and above all
// incremental recompilation — is built on these two structures, so the package
// deliberately trades caller convenience for strictness: enum fields reject the
// empty string, derived hashes are re-verified rather than trusted, and a
// duration budget that is not a genuine interval is rejected outright. A model
// that silently accepts a slightly-wrong value produces a video that is
// silently wrong, which is far more expensive to diagnose than a failed write.
package model

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// Validation failures callers are expected to branch on. Everything else is a
// plain fmt.Errorf with the offending field spelled out in a dotted path.
var (
	// ErrFixedDurationBudget means a budget was given as a single value rather
	// than an interval. See DurationBudget for why this cannot be allowed.
	ErrFixedDurationBudget = errors.New("duration_budget must be an interval, not a fixed value")
	// ErrDurationBudgetRange means the interval exists but its width is outside
	// the permitted band.
	ErrDurationBudgetRange = errors.New("duration_budget width is out of the permitted band")
	// ErrDependencyCycle means depends_on edges form a cycle.
	ErrDependencyCycle = errors.New("depends_on cycle")
	// ErrUnknownDependency means a depends_on edge points at a seg that does
	// not exist in the graph.
	ErrUnknownDependency = errors.New("depends_on references an unknown seg")
	// ErrStaleDerived means a stored content_hash or render_cache_key does not
	// match a recomputation, i.e. the seg was edited without re-sealing.
	ErrStaleDerived = errors.New("derived field is stale")
)

// Tolerance bounds on the duration budget, expressed in percent of the
// interval midpoint.
//
// The upper bound is an audio quality limit: time-stretching TTS output by more
// than 8% is audible and destroys naturalness. The lower bound exists for a
// different reason — a budget narrower than ±2% is blown by a single word
// substitution, which forces neighbouring segs to be re-rendered and defeats
// the incremental recompilation the interval was introduced to enable.
const (
	MaxTolerancePercent = 8
	MinTolerancePercent = 2
)

// MinTargetMS is the shortest target NewDurationBudget can turn into a usable
// band. Below it, ceil(0.92T) and floor(1.08T) land on the same whole
// millisecond and the band degenerates to a point.
//
// There is no way around this: widening the band to keep it open would push the
// tolerance past 8%, which is the limit the band exists to enforce. The bound
// is harmless in practice — no spoken syllable is 13 milliseconds long, so a
// smaller target is a caller bug, and Validate reports it as one.
const MinTargetMS = 13

// DurationBudget is the acceptable rendered duration of a seg as a closed
// interval in milliseconds.
//
// It is an interval rather than a single value because that is the only shape
// under which incremental recompilation is physically possible. A render cache
// hit requires both a matching render_cache_key and a cached artifact whose
// actual duration falls inside the budget; with a fixed budget the second
// condition degenerates into "the TTS engine produced exactly N milliseconds",
// which essentially never holds twice, so every edit would re-render the whole
// video.
type DurationBudget struct {
	MinMS int64 `json:"min_ms"`
	MaxMS int64 `json:"max_ms"`
}

// NewDurationBudget derives the standard ±8% band around a target duration.
//
// Both ends round inward, so the resulting interval is never wider than 8%.
// Rounding outward would push the real tolerance past the audio quality limit
// by a fraction of a percent, and that limit is the whole point of the number.
//
// A target below MinTargetMS yields a degenerate band that Validate rejects.
// The alternative — silently clamping a nonsensical target up to something
// expressible — would hide the caller's mistake behind a duration they never
// asked for.
func NewDurationBudget(targetMS int64) DurationBudget {
	lo := 100 - int64(MaxTolerancePercent)
	hi := 100 + int64(MaxTolerancePercent)
	return DurationBudget{
		MinMS: (targetMS*lo + 99) / 100, // ceil
		MaxMS: targetMS * hi / 100,      // floor
	}
}

// Validate reports whether the budget is a usable interval.
func (b DurationBudget) Validate() error {
	if b.MinMS <= 0 {
		return fmt.Errorf("duration_budget_ms.min_ms must be positive, got %d", b.MinMS)
	}
	if b.MaxMS == b.MinMS {
		return fmt.Errorf("%w: got a %dms point", ErrFixedDurationBudget, b.MinMS)
	}
	if b.MaxMS < b.MinMS {
		return fmt.Errorf("duration_budget_ms.max_ms %d is below min_ms %d", b.MaxMS, b.MinMS)
	}

	// Half-width as a fraction of the midpoint is (max-min)/(min+max); compare
	// in integers so the check cannot drift with floating point.
	span := b.MaxMS - b.MinMS
	sum := b.MinMS + b.MaxMS
	if 100*span > MaxTolerancePercent*sum {
		return fmt.Errorf("%w: [%d,%d] is wider than ±%d%%", ErrDurationBudgetRange, b.MinMS, b.MaxMS, MaxTolerancePercent)
	}
	if 100*span < MinTolerancePercent*sum {
		return fmt.Errorf("%w: [%d,%d] is narrower than ±%d%%, leaving no room for incremental reuse",
			ErrDurationBudgetRange, b.MinMS, b.MaxMS, MinTolerancePercent)
	}
	return nil
}

// Contains reports whether an actual rendered duration satisfies the budget.
// Both ends are inclusive.
func (b DurationBudget) Contains(ms int64) bool {
	return ms >= b.MinMS && ms <= b.MaxMS
}

// TargetMS is the midpoint of the interval, i.e. what the renderer should aim
// for before any time-stretching.
func (b DurationBudget) TargetMS() int64 { return (b.MinMS + b.MaxMS) / 2 }

// EmotionTag is the delivery hint handed to the TTS engine.
type EmotionTag string

// The closed set of emotion tags. The set is small on purpose: every value has
// to be mappable onto every provider we support, and a tag no provider honours
// is a tag that silently does nothing.
const (
	EmotionNeutral EmotionTag = "neutral"
	EmotionCalm    EmotionTag = "calm"
	EmotionWarm    EmotionTag = "warm"
	EmotionSerious EmotionTag = "serious"
	EmotionExcited EmotionTag = "excited"
	EmotionUrgent  EmotionTag = "urgent"
)

// Valid reports whether e is a known tag. The empty string is not valid: if
// both "" and "neutral" meant neutral, the same content would produce two
// different content hashes and the TTS cache would never fill up.
func (e EmotionTag) Valid() bool {
	switch e {
	case EmotionNeutral, EmotionCalm, EmotionWarm, EmotionSerious, EmotionExcited, EmotionUrgent:
		return true
	default:
		return false
	}
}

// Breath is the pause inserted after a seg.
type Breath string

// Breath levels. This is an enum rather than a millisecond count because a
// number would pretend the TTS engine honours a precise value; engines only
// expose coarse SSML pause levels, and PauseMS below is our own mapping, not a
// guarantee from the provider.
const (
	BreathNone  Breath = "none"
	BreathShort Breath = "short"
	BreathLong  Breath = "long"
)

// Valid reports whether b is a known breath level.
func (b Breath) Valid() bool {
	switch b {
	case BreathNone, BreathShort, BreathLong:
		return true
	default:
		return false
	}
}

// PauseMS is the nominal pause each level stands for.
func (b Breath) PauseMS() int64 {
	switch b {
	case BreathShort:
		return 250
	case BreathLong:
		return 600
	default:
		return 0
	}
}

// AudioSourceKind distinguishes synthesised audio from recorded material.
type AudioSourceKind string

// Audio source kinds. Only AudioTTS is reachable in the MVP; AudioRecording
// exists so that wiring up real human takes in V2 does not change this schema.
const (
	AudioTTS       AudioSourceKind = "tts"
	AudioRecording AudioSourceKind = "recording"
)

// AudioSource points at pre-existing audio for a seg.
//
// It is nil throughout the MVP — every seg is synthesised. The field is carried
// anyway so that the V2 human-material path can be added without migrating
// stored documents; the accepted cost is a field with no producer and no
// consumer today.
type AudioSource struct {
	Kind       AudioSourceKind `json:"kind"`
	URI        string          `json:"uri,omitempty"`
	InPointMS  int64           `json:"in_point_ms,omitempty"`
	OutPointMS int64           `json:"out_point_ms,omitempty"`
}

// Validate checks an audio source that is present.
func (a *AudioSource) Validate() error {
	switch a.Kind {
	case AudioTTS, AudioRecording:
	default:
		return fmt.Errorf("audio_source.kind %q is not a known kind", a.Kind)
	}
	if a.Kind == AudioRecording && a.URI == "" {
		return errors.New("audio_source.uri must be set for a recording")
	}
	if a.InPointMS < 0 {
		return fmt.Errorf("audio_source.in_point_ms must not be negative, got %d", a.InPointMS)
	}
	if a.OutPointMS != 0 && a.OutPointMS <= a.InPointMS {
		return fmt.Errorf("audio_source.out_point_ms %d must be after in_point_ms %d", a.OutPointMS, a.InPointMS)
	}
	return nil
}

// Seg is one independently renderable slice of the script.
//
// ContentHash and RenderCacheKey are derived: they are written by Project.Seal
// and re-verified by Project.Validate. They live on the struct rather than
// being computed on read because the store indexes RenderCacheKey, and an index
// needs a column.
type Seg struct {
	SegID string `json:"seg_id"`
	Text  string `json:"text"`
	// ContentHash identifies what is said. Derived; see ComputeContentHash.
	ContentHash    string         `json:"content_hash"`
	DurationBudget DurationBudget `json:"duration_budget_ms"`
	EmotionTag     EmotionTag     `json:"emotion_tag"`
	Breath         Breath         `json:"breath"`
	// VisualPromptSlot names the visual slot this seg fills. It holds the slot
	// name, never the prompt text: a prompt would have to enter ContentHash,
	// and then rewording a shot description would throw away the audio too.
	// No consumer in the MVP.
	VisualPromptSlot string `json:"visual_prompt_slot,omitempty"`
	// SubtitleBreaks are rune offsets into Text where a subtitle line may wrap.
	SubtitleBreaks []int `json:"subtitle_breaks,omitempty"`
	// DependsOn lists seg ids that must be resolved before this one.
	DependsOn []string `json:"depends_on,omitempty"`
	// ContinuityGroup names a run of segs that form one continuous physical
	// action, so a cut inside the group would be visible. Segs sharing a group
	// cannot be recompiled independently; see the recompile package.
	//
	// It is grouping metadata, not content: neither hash covers it.
	ContinuityGroup string `json:"continuity_group,omitempty"`
	// GenerationBatch names the segs that came out of a single multi-shot
	// generation call. The shots in a batch were conditioned on each other, so
	// regenerating one alone produces a shot that no longer matches its
	// neighbours.
	//
	// It is grouping metadata, not content: neither hash covers it.
	GenerationBatch string `json:"generation_batch,omitempty"`
	// RenderCacheKey identifies a reusable rendered artifact. Derived; see
	// ComputeRenderCacheKey. Consumed by the recompile engine's cache lookup;
	// no renderer produces the artifacts it resolves to yet.
	RenderCacheKey string `json:"render_cache_key"`
	// Protected marks a seg the user has locked; regeneration must not
	// overwrite it. No consumer in the MVP — there is no regeneration path yet.
	Protected bool `json:"protected"`
	// AudioSource is a V2 placeholder, always nil in the MVP.
	AudioSource *AudioSource `json:"audio_source,omitempty"`
}

// NewSeg builds a seg with the standard ±8% budget and neutral defaults.
// Derived fields are left empty; call Project.Seal to fill them.
func NewSeg(segID, text string, targetMS int64) Seg {
	return Seg{
		SegID:          segID,
		Text:           text,
		DurationBudget: NewDurationBudget(targetMS),
		EmotionTag:     EmotionNeutral,
		Breath:         BreathNone,
	}
}

// Validate checks every non-derived field. Derived fields are checked by
// Project.Validate, which has the render profile needed to recompute them.
func (s Seg) Validate() error {
	if s.SegID == "" {
		return errors.New("seg.seg_id must not be empty")
	}
	if s.Text == "" {
		return fmt.Errorf("seg %s: text must not be empty", s.SegID)
	}
	if err := s.DurationBudget.Validate(); err != nil {
		return fmt.Errorf("seg %s: %w", s.SegID, err)
	}
	if !s.EmotionTag.Valid() {
		return fmt.Errorf("seg %s: emotion_tag %q is not a known tag", s.SegID, s.EmotionTag)
	}
	if !s.Breath.Valid() {
		return fmt.Errorf("seg %s: breath %q is not a known level", s.SegID, s.Breath)
	}
	if err := s.validateSubtitleBreaks(); err != nil {
		return fmt.Errorf("seg %s: %w", s.SegID, err)
	}
	if s.AudioSource != nil {
		if err := s.AudioSource.Validate(); err != nil {
			return fmt.Errorf("seg %s: %w", s.SegID, err)
		}
	}

	seen := make(map[string]struct{}, len(s.DependsOn))
	for _, dep := range s.DependsOn {
		if dep == "" {
			return fmt.Errorf("seg %s: depends_on contains an empty id", s.SegID)
		}
		if _, dup := seen[dep]; dup {
			return fmt.Errorf("seg %s: depends_on lists %s twice", s.SegID, dep)
		}
		seen[dep] = struct{}{}
	}
	return nil
}

func (s Seg) validateSubtitleBreaks() error {
	if len(s.SubtitleBreaks) == 0 {
		return nil
	}
	limit := utf8.RuneCountInString(s.Text)
	prev := 0
	for i, at := range s.SubtitleBreaks {
		if at <= 0 || at >= limit {
			return fmt.Errorf("subtitle_breaks[%d] = %d is outside (0,%d)", i, at, limit)
		}
		if at <= prev {
			return fmt.Errorf("subtitle_breaks[%d] = %d does not increase past %d", i, at, prev)
		}
		prev = at
	}
	return nil
}

// CanReuse reports whether a cached artifact may be reused for this seg.
//
// Both halves matter. The key proves the artifact was produced from the same
// content under the same pipeline; the budget check proves its actual duration
// is still acceptable. Splitting the decision this way is what lets a budget
// change, or TTS jitter between runs, leave the cache intact.
func (s Seg) CanReuse(cachedKey string, cachedDurationMS int64) bool {
	if s.RenderCacheKey == "" || cachedKey != s.RenderCacheKey {
		return false
	}
	return s.DurationBudget.Contains(cachedDurationMS)
}
