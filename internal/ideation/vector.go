package ideation

import (
	"cmp"
	"hash/fnv"
	"math"
	"slices"
)

const defaultEmbedDim = 16

// EmbedFromCard builds a deterministic pseudo-embedding from structure labels.
//
// Real embeddings would come from a model; for MVP recall regression tests we
// need a stable vector that changes when structure changes. Each dimension is
// seeded from one field hash so similar structures cluster loosely.
func EmbedFromCard(fields ...string) []float64 {
	vec := make([]float64, defaultEmbedDim)
	for i, f := range fields {
		if i >= defaultEmbedDim {
			break
		}
		h := fnv.New64a()
		_, _ = h.Write([]byte(f))
		vec[i] = float64(h.Sum64()%1000) / 1000.0
	}
	return normalize(vec)
}

func normalize(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return v
	}
	n := math.Sqrt(sum)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

// CosineSimilarity returns the cosine of the angle between two vectors.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// RecallMatch is one card returned by vector recall.
type RecallMatch struct {
	Card       StructureCard `json:"card"`
	Similarity float64       `json:"similarity"`
}

// RecallTopK returns up to k cards ordered by cosine similarity to query.
func RecallTopK(query []float64, cards []StructureCard, k int) []RecallMatch {
	if k <= 0 {
		k = 5
	}
	matches := make([]RecallMatch, 0, len(cards))
	for _, c := range cards {
		if len(c.Embedding) == 0 {
			continue
		}
		matches = append(matches, RecallMatch{
			Card:       c,
			Similarity: CosineSimilarity(query, c.Embedding),
		})
	}
	slices.SortFunc(matches, func(a, b RecallMatch) int {
		if c := cmp.Compare(b.Similarity, a.Similarity); c != 0 {
			return c
		}
		return cmp.Compare(a.Card.ID, b.Card.ID)
	})
	if len(matches) > k {
		matches = matches[:k]
	}
	return matches
}
