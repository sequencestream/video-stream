package model

import (
	"fmt"
	"sort"
	"strings"
)

// renderOrder returns the seg ids in dependency order.
//
// Ties are broken by seg id so the order is reproducible across runs: an order
// that shuffles between runs would make render cache hits depend on map
// iteration, which is the kind of nondeterminism that only shows up in
// production.
func renderOrder(segs []Seg) ([]string, error) {
	// Duplicate ids are rejected here and not only in Project.Validate, because
	// this function is reachable through the exported Project.RenderOrder. With
	// a duplicate id the maps below hold fewer entries than segs, every
	// in-degree still reaches zero, and the completeness check at the bottom
	// would fire with no cycle to report.
	index := make(map[string]Seg, len(segs))
	for _, s := range segs {
		if _, dup := index[s.SegID]; dup {
			return nil, fmt.Errorf("seg_id %s appears more than once", s.SegID)
		}
		index[s.SegID] = s
	}

	remaining := make(map[string]int, len(segs))
	dependents := make(map[string][]string, len(segs))
	for _, s := range segs {
		remaining[s.SegID] = len(s.DependsOn)
		for _, dep := range s.DependsOn {
			if _, ok := index[dep]; !ok {
				return nil, fmt.Errorf("%w: seg %s depends on %s", ErrUnknownDependency, s.SegID, dep)
			}
			dependents[dep] = append(dependents[dep], s.SegID)
		}
	}

	ready := make([]string, 0, len(segs))
	for id, n := range remaining {
		if n == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(segs))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)

		var freed []string
		for _, next := range dependents[id] {
			remaining[next]--
			if remaining[next] == 0 {
				freed = append(freed, next)
			}
		}
		if len(freed) > 0 {
			ready = append(ready, freed...)
			sort.Strings(ready)
		}
	}

	if len(order) != len(segs) {
		return nil, cycleError(index, remaining)
	}
	return order, nil
}

// cycleError walks the unresolved subgraph to name an actual cycle.
//
// Reporting only "a cycle exists" would leave the caller to find it by hand in
// a graph that may hold hundreds of segs, so the message carries the full path.
func cycleError(index map[string]Seg, remaining map[string]int) error {
	stuck := make([]string, 0, len(remaining))
	for id, n := range remaining {
		if n > 0 {
			stuck = append(stuck, id)
		}
	}
	sort.Strings(stuck)

	// Follow dependency edges inside the stuck set until a node repeats. Every
	// node here still has at least one unresolved dependency, so the walk
	// cannot run out of edges before it closes a loop.
	inStuck := make(map[string]struct{}, len(stuck))
	for _, id := range stuck {
		inStuck[id] = struct{}{}
	}

	seen := make(map[string]int)
	var path []string
	cur := stuck[0]
	for {
		if at, ok := seen[cur]; ok {
			loop := append(append([]string{}, path[at:]...), cur)
			return fmt.Errorf("%w: %s", ErrDependencyCycle, strings.Join(loop, " -> "))
		}
		seen[cur] = len(path)
		path = append(path, cur)

		next := ""
		deps := append([]string{}, index[cur].DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if _, ok := inStuck[dep]; ok {
				next = dep
				break
			}
		}
		if next == "" {
			return fmt.Errorf("%w: unresolved segs %s", ErrDependencyCycle, strings.Join(stuck, ", "))
		}
		cur = next
	}
}
