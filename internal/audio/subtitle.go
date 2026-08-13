package audio

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sequencestream/video-stream/internal/model"
)

// PlatformSpec defines subtitle and loudness targets per distribution platform.
type PlatformSpec struct {
	Platform        string       `json:"platform"`
	TargetLUFS      float64      `json:"target_lufs"`
	LUFSTolerance   float64      `json:"lufs_tolerance"`
	MaxCharsPerLine int          `json:"max_chars_per_line"`
	FontFamily      string       `json:"font_family"`
	PreferredMode   SubtitleMode `json:"preferred_mode"`
}

// DefaultPlatformSpecs returns MVP defaults.
func DefaultPlatformSpecs() []PlatformSpec {
	return []PlatformSpec{
		{Platform: "youtube", TargetLUFS: -14, LUFSTolerance: 0.5, MaxCharsPerLine: 42, FontFamily: "Roboto", PreferredMode: SubtitleSoft},
		{Platform: "douyin", TargetLUFS: -16, LUFSTolerance: 0.5, MaxCharsPerLine: 18, FontFamily: "PingFang SC", PreferredMode: SubtitleBurnIn},
		{Platform: "bilibili", TargetLUFS: -14, LUFSTolerance: 0.5, MaxCharsPerLine: 36, FontFamily: "Source Han Sans", PreferredMode: SubtitleSoft},
	}
}

// SpecFor returns a platform spec by name.
func SpecFor(platform string) (PlatformSpec, bool) {
	for _, s := range DefaultPlatformSpecs() {
		if strings.EqualFold(s.Platform, platform) {
			return s, true
		}
	}
	return PlatformSpec{}, false
}

// SegmentSubtitle builds cue lines honoring subtitle_breaks and breath points.
func SegmentSubtitle(seg model.Seg, tokens []model.Token, spec PlatformSpec) []string {
	if len(tokens) == 0 {
		return nil
	}
	text := strings.TrimSpace(seg.Text)
	if text == "" {
		text = strings.Join(tokenTexts(tokens), " ")
	}
	breaks := seg.SubtitleBreaks
	var lines []string
	startRune := 0
	runes := []rune(text)
	for _, br := range breaks {
		if br > len(runes) {
			break
		}
		chunk := strings.TrimSpace(string(runes[startRune:br]))
		if chunk != "" {
			lines = append(lines, wrapLines(chunk, spec.MaxCharsPerLine)...)
		}
		startRune = br
	}
	if startRune < len(runes) {
		lines = append(lines, wrapLines(strings.TrimSpace(string(runes[startRune:])), spec.MaxCharsPerLine)...)
	}
	return lines
}

func tokenTexts(tokens []model.Token) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = t.Text
	}
	return out
}

func wrapLines(text string, max int) []string {
	if max <= 0 || utf8.RuneCountInString(text) <= max {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) <= 1 {
		return wrapRunes(text, max)
	}
	var lines []string
	var cur string
	for _, w := range words {
		next := strings.TrimSpace(cur + " " + w)
		if utf8.RuneCountInString(next) > max && cur != "" {
			lines = append(lines, cur)
			if utf8.RuneCountInString(w) > max {
				wrapped := wrapRunes(w, max)
				lines = append(lines, wrapped[:len(wrapped)-1]...)
				cur = wrapped[len(wrapped)-1]
			} else {
				cur = w
			}
		} else {
			cur = next
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func wrapRunes(text string, max int) []string {
	runes := []rune(text)
	var lines []string
	for len(runes) > max {
		lines = append(lines, string(runes[:max]))
		runes = runes[max:]
	}
	if len(runes) > 0 {
		lines = append(lines, string(runes))
	}
	return lines
}

// FormatWebVTT renders soft subtitle cues.
func FormatWebVTT(segID string, lines []string, startMS, endMS int64) string {
	if len(lines) == 0 || endMS <= startMS {
		return ""
	}
	identifier := ""
	if strings.TrimSpace(segID) != "" {
		identifier = strings.TrimSpace(segID) + "\n"
	}
	return fmt.Sprintf("%s%s --> %s\n%s\n",
		identifier, formatTS(startMS), formatTS(endMS), strings.Join(lines, "\n"))
}

func formatTS(ms int64) string {
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}
