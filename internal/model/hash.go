package model

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"strconv"
)

// Prefixes on the two derived keys. They make a stored value self-describing:
// when a hashing rule changes the prefix changes with it, so old values are
// recognisable and can be recomputed instead of silently failing to match.
const (
	contentHashPrefix    = "ch1:"
	renderCacheKeyPrefix = "rk1:"
)

// RenderProfile identifies the pipeline configuration that produced a rendered
// artifact. It participates in the render cache key so that switching voices or
// upgrading the renderer does not serve stale output.
//
// The MVP passes the zero value because no renderer exists yet. Introducing one
// will invalidate every key at once, which is harmless precisely because there
// are no cached artifacts to lose.
type RenderProfile struct {
	Voice    string `json:"voice,omitempty"`
	Renderer string `json:"renderer,omitempty"`
}

// ComputeContentHash derives the identity of what a seg says.
//
// It covers text, emotion, breath and audio source — everything that changes
// the audio. It deliberately excludes seg_id, so two segs with identical
// wording share one TTS artifact; excluding it is the whole reason the cache
// fills up. It also excludes duration_budget, subtitle_breaks,
// visual_prompt_slot, depends_on and protected, none of which change what is
// spoken.
func ComputeContentHash(s Seg) string {
	h := sha256.New()
	writeField(h, "v", "1")
	writeField(h, "text", s.Text)
	writeField(h, "emotion", string(s.EmotionTag))
	writeField(h, "breath", string(s.Breath))
	if s.AudioSource == nil {
		writeField(h, "audio", "")
	} else {
		writeField(h, "audio.kind", string(s.AudioSource.Kind))
		writeField(h, "audio.uri", s.AudioSource.URI)
		writeField(h, "audio.in", strconv.FormatInt(s.AudioSource.InPointMS, 10))
		writeField(h, "audio.out", strconv.FormatInt(s.AudioSource.OutPointMS, 10))
	}
	return contentHashPrefix + hex.EncodeToString(h.Sum(nil))
}

// ComputeRenderCacheKey derives the identity of a reusable rendered artifact.
//
// It covers the content hash plus everything baked into the rendered frames:
// the visual slot, the subtitle wrap positions, and the render profile.
//
// It pointedly does NOT cover duration_budget. Folding the budget in would mean
// that widening or nudging a budget throws away artifacts whose actual duration
// still fits, which is exactly the incremental reuse the interval exists to
// enable. The budget is checked separately, against the artifact's real
// duration, in Seg.CanReuse.
func ComputeRenderCacheKey(s Seg, profile RenderProfile) string {
	h := sha256.New()
	writeField(h, "v", "1")
	writeField(h, "content", ComputeContentHash(s))
	writeField(h, "visual_slot", s.VisualPromptSlot)
	writeField(h, "breaks", formatInts(s.SubtitleBreaks))
	writeField(h, "voice", profile.Voice)
	writeField(h, "renderer", profile.Renderer)
	return renderCacheKeyPrefix + hex.EncodeToString(h.Sum(nil))
}

// writeField appends a length-prefixed name/value pair.
//
// The length prefixes are what make the encoding unambiguous: plain
// concatenation would hash ("ab","c") and ("a","bc") to the same digest, so a
// seg could inherit another seg's cached audio by an accident of spelling.
func writeField(h hash.Hash, name, value string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(name)))
	h.Write(n[:])
	h.Write([]byte(name))
	binary.BigEndian.PutUint64(n[:], uint64(len(value)))
	h.Write(n[:])
	h.Write([]byte(value))
}

func formatInts(xs []int) string {
	if len(xs) == 0 {
		return ""
	}
	out := make([]byte, 0, len(xs)*4)
	for i, x := range xs {
		if i > 0 {
			out = append(out, ',')
		}
		out = strconv.AppendInt(out, int64(x), 10)
	}
	return string(out)
}
