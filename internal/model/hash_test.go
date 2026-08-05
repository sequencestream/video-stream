package model_test

import (
	"strings"
	"testing"

	"github.com/sequencestream/video-stream/internal/model"
)

func TestContentHashIsStableForTheSameInput(t *testing.T) {
	seg := model.NewSeg("s1", "增量重编译是这件事的支点", 2000)

	first := model.ComputeContentHash(seg)
	for i := 0; i < 100; i++ {
		if got := model.ComputeContentHash(seg); got != first {
			t.Fatalf("call %d returned %q, want %q", i, got, first)
		}
	}
	if !strings.HasPrefix(first, "ch1:") {
		t.Fatalf("content hash %q is missing its version prefix", first)
	}

	// A separately constructed but equal seg must hash identically, otherwise
	// the hash is capturing identity rather than content.
	twin := model.NewSeg("a-completely-different-id", "增量重编译是这件事的支点", 2000)
	if got := model.ComputeContentHash(twin); got != first {
		t.Fatalf("equal content hashed differently: %q vs %q", got, first)
	}
}

// Any character change at all must move the hash, including whitespace and
// characters outside the BMP.
func TestContentHashChangesWithAnyCharacterEdit(t *testing.T) {
	base := "hello world"
	edits := []struct {
		name string
		text string
	}{
		{"substituted a letter", "hallo world"},
		{"deleted a letter", "hell world"},
		{"appended a letter", "hello worlds"},
		{"changed case", "Hello world"},
		{"doubled a space", "hello  world"},
		{"trailing space", "hello world "},
		{"leading space", " hello world"},
		{"swapped in a full-width space", "hello\u3000world"},
		{"appended an emoji", "hello world\U0001F600"},
		{"appended a zero-width joiner", "hello world\u200d"},
	}

	baseHash := model.ComputeContentHash(model.NewSeg("s1", base, 1000))
	seen := map[string]string{baseHash: "base"}
	for _, edit := range edits {
		t.Run(edit.name, func(t *testing.T) {
			got := model.ComputeContentHash(model.NewSeg("s1", edit.text, 1000))
			if prior, dup := seen[got]; dup {
				t.Fatalf("%s hashed the same as %s", edit.name, prior)
			}
			seen[got] = edit.name
		})
	}
}

func TestContentHashCoversDeliveryButNotLayout(t *testing.T) {
	base := model.NewSeg("s1", "hello", 1000)
	baseHash := model.ComputeContentHash(base)

	changes := []struct {
		name  string
		mutil func(*model.Seg)
	}{
		{"emotion_tag", func(s *model.Seg) { s.EmotionTag = model.EmotionUrgent }},
		{"breath", func(s *model.Seg) { s.Breath = model.BreathLong }},
		{"audio_source", func(s *model.Seg) {
			s.AudioSource = &model.AudioSource{Kind: model.AudioRecording, URI: "file:///take1.wav"}
		}},
	}
	for _, c := range changes {
		t.Run("changes with "+c.name, func(t *testing.T) {
			seg := base
			c.mutil(&seg)
			if got := model.ComputeContentHash(seg); got == baseHash {
				t.Fatalf("%s did not move the content hash", c.name)
			}
		})
	}

	// Nothing below changes what is spoken, so none of it may bust the TTS
	// cache. duration_budget in particular is a constraint on the render, not
	// on the content.
	unchanged := []struct {
		name  string
		mutil func(*model.Seg)
	}{
		{"seg_id", func(s *model.Seg) { s.SegID = "renamed" }},
		{"duration_budget", func(s *model.Seg) { s.DurationBudget = model.NewDurationBudget(9000) }},
		{"subtitle_breaks", func(s *model.Seg) { s.SubtitleBreaks = []int{2} }},
		{"visual_prompt_slot", func(s *model.Seg) { s.VisualPromptSlot = "hero-shot" }},
		{"depends_on", func(s *model.Seg) { s.DependsOn = []string{"s0"} }},
		{"protected", func(s *model.Seg) { s.Protected = true }},
		{"render_cache_key", func(s *model.Seg) { s.RenderCacheKey = "rk1:stale" }},
	}
	for _, c := range unchanged {
		t.Run("ignores "+c.name, func(t *testing.T) {
			seg := base
			c.mutil(&seg)
			if got := model.ComputeContentHash(seg); got != baseHash {
				t.Fatalf("%s moved the content hash", c.name)
			}
		})
	}
}

// Without length prefixes the encoding would be a plain concatenation, and two
// different inputs could hash alike by an accident of spelling. Voice and
// renderer are adjacent free-form strings, so they are where that actually
// bites: "voice|arenderer|renderer|" and "voice|a|renderer|renderer"
// concatenate to the same bytes.
func TestHashEncodesFieldBoundaries(t *testing.T) {
	seg := model.NewSeg("s1", "hello", 1000)

	collidingPair := model.ComputeRenderCacheKey(seg, model.RenderProfile{Voice: "arenderer"}) ==
		model.ComputeRenderCacheKey(seg, model.RenderProfile{Voice: "a", Renderer: "renderer"})
	if collidingPair {
		t.Fatal("field boundaries are not encoded: two different render profiles collided")
	}
}

func TestRenderCacheKeyCoversRenderedAppearance(t *testing.T) {
	base := model.NewSeg("s1", "hello world", 1000)
	profile := model.RenderProfile{}
	baseKey := model.ComputeRenderCacheKey(base, profile)

	if !strings.HasPrefix(baseKey, "rk1:") {
		t.Fatalf("render cache key %q is missing its version prefix", baseKey)
	}
	if got := model.ComputeRenderCacheKey(base, profile); got != baseKey {
		t.Fatalf("render cache key is not stable: %q vs %q", got, baseKey)
	}

	t.Run("changes with the text", func(t *testing.T) {
		seg := base
		seg.Text = "hello worlds"
		if model.ComputeRenderCacheKey(seg, profile) == baseKey {
			t.Fatal("a text edit did not move the render cache key")
		}
	})
	t.Run("changes with subtitle_breaks", func(t *testing.T) {
		seg := base
		seg.SubtitleBreaks = []int{5}
		if model.ComputeRenderCacheKey(seg, profile) == baseKey {
			t.Fatal("a subtitle break did not move the render cache key")
		}
	})
	t.Run("changes with visual_prompt_slot", func(t *testing.T) {
		seg := base
		seg.VisualPromptSlot = "hero-shot"
		if model.ComputeRenderCacheKey(seg, profile) == baseKey {
			t.Fatal("a visual slot change did not move the render cache key")
		}
	})
	t.Run("changes with the render profile", func(t *testing.T) {
		if model.ComputeRenderCacheKey(base, model.RenderProfile{Voice: "xiaoyun"}) == baseKey {
			t.Fatal("a voice change did not move the render cache key")
		}
	})
}

// This is the assertion the incremental recompilation story rests on. If the
// budget entered the key, nudging a budget would discard artifacts whose real
// duration still fits, and no cache would ever survive an edit.
func TestRenderCacheKeyIgnoresTheDurationBudget(t *testing.T) {
	base := model.NewSeg("s1", "hello world", 1000)
	baseKey := model.ComputeRenderCacheKey(base, model.RenderProfile{})

	widened := base
	widened.DurationBudget = model.NewDurationBudget(1200)
	if got := model.ComputeRenderCacheKey(widened, model.RenderProfile{}); got != baseKey {
		t.Fatal("changing the duration budget moved the render cache key")
	}
}
