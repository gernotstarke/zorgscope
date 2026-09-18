package domain

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Query is a search as the matcher reads it: the words to find, and the kind the reserved words
// asked for (FR-12.1).
type Query struct {
	// Words are the free words, lower-cased, in the order typed; never empty strings.
	Words []string
	// Kind is set when the query held a reserved kind word; "" means both kinds.
	Kind Kind
}

// ParseQuery splits q on whitespace, lower-cases every word, and takes the reserved words out:
// "issue" and "issues" set Kind to KindIssue, "pr", "prs" and "pull" set it to KindPR. A query
// with both kinds keeps the last one typed. Everything else is a word to find.
func ParseQuery(q string) Query {
	var out Query
	for _, w := range strings.Fields(strings.ToLower(q)) {
		switch w {
		case "issue", "issues":
			out.Kind = KindIssue
		case "pr", "prs", "pull":
			out.Kind = KindPR
		default:
			out.Words = append(out.Words, w)
		}
	}
	return out
}

// Hit is one item the query matched, with the evidence.
type Hit struct {
	// Item is the matched item.
	Item Item
	// Score is the summed match score across all query words.
	Score int
	// Matched names the fields that matched, in the order of matchedFields and without repeats.
	// nil when only the kind matched.
	Matched []string
	// TitleSpans are the byte ranges [start, end) of Item.Title the words matched, merged where
	// they touch or overlap, in order — what a page wraps in <mark>. nil when lower-casing the
	// title changes its byte length, which is a cheap sufficient condition for the ranges mapping
	// back rather than a guarantee of it: a title that mixes a rune growing with a rune shrinking
	// — U+023A lower-cases from two bytes to three, U+212A from three to one — keeps its total
	// length while the offsets between them shift. The cost of that title is one replacement
	// character in the marked title and never markup, since each run is escaped on its own and
	// the offsets stay inside the title.
	TitleSpans [][2]int
}

// The score each field contributes per word. A title match at a word start is worth most; the
// summary and the repository name are tie-breakers rather than reasons.
const (
	scoreTitleWordStart = 4
	scoreTitleElsewhere = 2
	scoreContributor    = 3
	scoreLabel          = 3
	scoreRepository     = 1
	scoreSummary        = 1
)

// matchedFields is the fixed order of Hit.Matched.
var matchedFields = []string{"title", "contributor", "label", "repository", "summary"}

// Search ranks items against q. Every word must match at least one field; an item that fails a
// word is out. The kind, when set, filters and scores nothing. A query with no words and no kind
// returns nil. Items are never mutated.
func Search(items []Item, q Query) []Hit {
	if len(q.Words) == 0 && q.Kind == "" {
		return nil
	}
	var hits []Hit
	for _, it := range items {
		if q.Kind != "" && it.Kind != q.Kind {
			continue
		}
		if h, ok := match(it, q.Words); ok {
			hits = append(hits, h)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if !a.Item.UpdatedAt.Equal(b.Item.UpdatedAt) {
			return a.Item.UpdatedAt.After(b.Item.UpdatedAt)
		}
		if a.Item.Repo != b.Item.Repo {
			return a.Item.Repo < b.Item.Repo
		}
		return a.Item.Number < b.Item.Number
	})
	return hits
}

// match scores one item against every word, and reports false as soon as a word matches nothing.
func match(it Item, words []string) (Hit, bool) {
	h := Hit{Item: it}
	title := strings.ToLower(it.Title)
	// Equal byte length means the lower-cased offsets can be reused on the original; see
	// Hit.TitleSpans for the exotic title this cheap check admits, and what it costs there.
	mappable := len(title) == len(it.Title)
	author := strings.ToLower(it.Author)
	repo := strings.ToLower(it.Repo)
	summary := strings.ToLower(it.Summary)
	labels := make([]string, len(it.Labels))
	for i, l := range it.Labels {
		labels[i] = strings.ToLower(l)
	}
	matched := make(map[string]bool, len(matchedFields))
	var spans [][2]int
	for _, w := range words {
		score := 0
		if s := titleScore(title, w); s > 0 {
			score += s
			matched["title"] = true
			if mappable {
				spans = append(spans, occurrences(title, w)...)
			}
		}
		if strings.Contains(author, w) {
			score += scoreContributor
			matched["contributor"] = true
		}
		for _, l := range labels {
			if strings.Contains(l, w) {
				score += scoreLabel
				matched["label"] = true
				break
			}
		}
		if strings.Contains(repo, w) {
			score += scoreRepository
			matched["repository"] = true
		}
		if strings.Contains(summary, w) {
			score += scoreSummary
			matched["summary"] = true
		}
		if score == 0 {
			return Hit{}, false
		}
		h.Score += score
	}
	for _, f := range matchedFields {
		if matched[f] {
			h.Matched = append(h.Matched, f)
		}
	}
	if mappable {
		h.TitleSpans = mergeSpans(spans)
	}
	return h, true
}

// titleScore is scoreTitleWordStart when w occurs at a word start of title, scoreTitleElsewhere
// when it occurs only inside words, and 0 when it does not occur.
func titleScore(title, w string) int {
	best := 0
	for _, span := range occurrences(title, w) {
		if atWordStart(title, span[0]) {
			return scoreTitleWordStart
		}
		best = scoreTitleElsewhere
	}
	return best
}

// atWordStart reports whether the byte at is the start of the string or follows something that
// is neither a letter nor a digit.
func atWordStart(s string, at int) bool {
	if at == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:at])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// occurrences is every [start, end) at which w occurs in s, overlapping ones included, in order.
// w is never empty in practice, since ParseQuery never yields an empty word; even so, the loop
// advances by one byte on every iteration, so it always terminates.
func occurrences(s, w string) [][2]int {
	var out [][2]int
	for from := 0; from <= len(s); {
		i := strings.Index(s[from:], w)
		if i < 0 {
			return out
		}
		at := from + i
		out = append(out, [2]int{at, at + len(w)})
		from = at + 1
	}
	return out
}

// mergeSpans sorts spans and joins the ones that touch or overlap; nil for none.
func mergeSpans(spans [][2]int) [][2]int {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	out := [][2]int{spans[0]}
	for _, s := range spans[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1] {
			last[1] = max(last[1], s[1])
			continue
		}
		out = append(out, s)
	}
	return out
}
