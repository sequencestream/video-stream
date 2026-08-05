package ideation

import "strings"

// EdgeRel identifies how two structure cards relate in the graph.
type EdgeRel string

const (
	// RelSimilar links cards with comparable overall structure.
	RelSimilar EdgeRel = "similar"
	// RelVariant links a card to a minor structural variation.
	RelVariant EdgeRel = "variant"
	// RelDerived links a card derived from another through migration.
	RelDerived EdgeRel = "derived"
)

// Edge is one directed relationship between structure cards.
type Edge struct {
	FromID string  `json:"from_id"`
	ToID   string  `json:"to_id"`
	Rel    EdgeRel `json:"rel"`
}

func (e Edge) normalise() Edge {
	e.FromID = strings.TrimSpace(e.FromID)
	e.ToID = strings.TrimSpace(e.ToID)
	if e.Rel == "" {
		e.Rel = RelSimilar
	}
	return e
}

// Neighbors returns cards linked to cardID in either direction, filtered by rel.
func Neighbors(cardID string, edges []Edge, cards map[string]StructureCard, rel EdgeRel) []StructureCard {
	out := make([]StructureCard, 0)
	seen := map[string]struct{}{cardID: {}}
	for _, e := range edges {
		if rel != "" && e.Rel != rel {
			continue
		}
		other := ""
		switch cardID {
		case e.FromID:
			other = e.ToID
		case e.ToID:
			other = e.FromID
		default:
			continue
		}
		if _, dup := seen[other]; dup {
			continue
		}
		if c, ok := cards[other]; ok {
			out = append(out, c)
			seen[other] = struct{}{}
		}
	}
	return out
}
