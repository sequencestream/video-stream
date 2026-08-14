package filler

import (
	"sort"
	"strings"
	"unicode"
)

// DefaultWords are the sounds that carry no meaning in any reading: hesitation
// noises, not words.
//
// The list is deliberately short. Everything a speaker might have meant lives
// in AggressiveWords instead, because a tool that silently deletes "就是" from
// "就是这个道理" has not cleaned up the take, it has changed what was said.
var DefaultWords = []string{
	// Chinese hesitation sounds.
	"嗯", "呃", "额", "唔", "呐", "诶", "欸",
	// English hesitation sounds.
	"um", "uhm", "uh", "er", "erm", "ah", "eh", "hmm", "mm",
	// Japanese and Korean fillers, for multilingual takes.
	"えーと", "あの", "그", "어",
}

// AggressiveWords are real words used as verbal padding. Cutting them tightens
// a take considerably and occasionally removes something the speaker meant, so
// they are opt-in through -aggressive.
var AggressiveWords = []string{
	// Chinese verbal padding.
	"那个", "这个", "就是", "就是说", "然后呢", "对吧", "是吧",
	"你知道", "怎么说呢", "反正", "其实吧", "这样子",
	// English verbal padding.
	"like", "you know", "i mean", "sort of", "kind of",
	"basically", "actually", "literally", "right", "okay so",
}

// normalize reduces a token to what should be compared: lower case, no
// punctuation, no spaces.
//
// Spaces go because word timings arrive one word at a time and a phrase like
// "you know" has to match the pair of them; punctuation goes because the
// recognizer attaches it to whichever word ends the sentence, and "um," is the
// same hesitation as "um".
func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// vocabulary is a filler word set prepared for matching.
type vocabulary struct {
	phrases map[string]string // normalized form -> original spelling
	maxLen  int               // longest phrase, in words, to bound the window
}

// newVocabulary builds the match set from a word list, a set of additions and a
// set of removals.
func newVocabulary(base, extra, keep []string) vocabulary {
	excluded := make(map[string]bool, len(keep))
	for _, w := range keep {
		if n := normalize(w); n != "" {
			excluded[n] = true
		}
	}

	v := vocabulary{phrases: make(map[string]string), maxLen: 1}
	for _, list := range [][]string{base, extra} {
		for _, w := range list {
			n := normalize(w)
			if n == "" || excluded[n] {
				continue
			}
			v.phrases[n] = strings.TrimSpace(w)
			if words := len(strings.Fields(w)); words > v.maxLen {
				v.maxLen = words
			}
			// A Chinese phrase has no spaces to count, so bound the window by
			// its character length instead: "就是说" may arrive as three words.
			if runes := len([]rune(n)); runes > v.maxLen {
				v.maxLen = runes
			}
		}
	}
	return v
}

// Words lists the vocabulary's entries in their original spelling, sorted.
func (v vocabulary) Words() []string {
	out := make([]string, 0, len(v.phrases))
	for _, original := range v.phrases {
		out = append(out, original)
	}
	sort.Strings(out)
	return out
}

// lookup reports whether the normalized text is a filler phrase.
func (v vocabulary) lookup(normalized string) (string, bool) {
	original, ok := v.phrases[normalized]
	return original, ok
}
