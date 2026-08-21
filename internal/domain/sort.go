package domain

import "sort"

// Evaluated pairs an item with its evaluation.
type Evaluated struct {
	Item Item
	Eval Evaluation
}

// SortByUrgency orders by level (most urgent first), then newest first (FR-2.5).
func SortByUrgency(list []Evaluated) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Eval.Level != list[j].Eval.Level {
			return list[i].Eval.Level > list[j].Eval.Level
		}
		return list[i].Item.CreatedAt.After(list[j].Item.CreatedAt)
	})
}

// FilterAttention keeps items whose level needs attention.
func FilterAttention(list []Evaluated) []Evaluated {
	out := make([]Evaluated, 0, len(list))
	for _, e := range list {
		if e.Eval.Level.NeedsAttention() {
			out = append(out, e)
		}
	}
	return out
}

// Cap limits the list to n entries (n <= 0: unlimited) and reports how many were cut (FR-2.5 AC4).
func Cap(list []Evaluated, n int) (shown []Evaluated, overflow int) {
	if n <= 0 || len(list) <= n {
		return list, 0
	}
	return list[:n], len(list) - n
}
