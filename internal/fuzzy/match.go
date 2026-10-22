// Package fuzzy finds paths by a few typed characters (SPEC §12.4): a
// matcher that scores and highlights, and a background index of ~.
// Standard library only.
package fuzzy

import (
	"cmp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Result is one match: its score (higher is better) and the matched rune
// positions in the path, ascending, for highlighting.
type Result struct {
	Score     int
	Positions []int
}

// Hit is a ranked path. Positions are not computed here (ranking scores
// tens of thousands of paths per keystroke); Highlight gives them for the
// few that are shown.
type Hit struct {
	Path  string
	Score int
}

const (
	bonusConsecutive  = 3 // right after the previous matched character
	bonusSegmentStart = 3 // at the start of a segment (after / . _ -)
	bonusBaseName     = 1 // in the base name
	none              = -1 << 30
)

// query is a parsed query: its terms and case rule.
type query struct {
	terms     [][]rune
	sensitive bool
}

func parse(q string) query {
	var out query
	for _, t := range strings.Fields(q) {
		out.terms = append(out.terms, []rune(t))
	}
	out.sensitive = strings.IndexFunc(q, unicode.IsUpper) >= 0
	return out
}

// scorer holds the scratch buffers of the dynamic programming, reused
// across paths so ranking allocates (almost) nothing per path.
type scorer struct {
	q         query
	hay       []rune
	bonus     []int
	prev, cur []int
	baseRunes int
}

func newScorer(q string) *scorer { return &scorer{q: parse(q)} }

// prepare lowercases path into hay (rune by rune, so hay stays aligned
// with path) and computes each position's bonus, in one pass.
func (s *scorer) prepare(path string) {
	if cap(s.hay) < len(path) {
		s.hay = make([]rune, 0, len(path))
		s.bonus, s.prev, s.cur = make([]int, len(path)), make([]int, len(path)), make([]int, len(path))
	}
	hay, bonus := s.hay[:0], s.bonus[:0]
	fold := !s.q.sensitive
	prev, base := '/', 0 // base: rune index of the base name
	for _, r := range path {
		b := 0
		if isBoundary(prev) {
			b += bonusSegmentStart
		}
		if r == '/' {
			base = len(hay) + 1
		}
		prev = r
		if fold {
			if r < utf8.RuneSelf { // ASCII fast path
				if 'A' <= r && r <= 'Z' {
					r += 'a' - 'A'
				}
			} else {
				r = unicode.ToLower(r)
			}
		}
		hay = append(hay, r)
		bonus = append(bonus, b)
	}
	for j := base; j < len(bonus); j++ {
		bonus[j] += bonusBaseName
	}
	s.hay, s.bonus = hay, bonus
	s.prev, s.cur = s.prev[:len(hay)], s.cur[:len(hay)]
}

// score is the path's total score over every term, or false.
func (s *scorer) score(path string) (int, bool) {
	if len(s.q.terms) == 0 {
		return 0, true
	}
	s.prepare(path)
	total := 0
	for _, t := range s.q.terms {
		if !isSubsequence(t, s.hay) {
			return 0, false
		}
		total += s.scoreTerm(t)
	}
	return total, true
}

// scoreTerm is term without the placement: the hot loop of ranking.
func (s *scorer) scoreTerm(q []rune) int {
	hay, bonus, prev, cur := s.hay, s.bonus, s.prev, s.cur
	n := len(hay)
	for j := 0; j < n; j++ {
		prev[j] = none
		if hay[j] == q[0] {
			prev[j] = bonus[j]
		}
	}
	for _, c := range q[1:] {
		bestV := none // best prev[k] for k < j-1
		for j := 0; j < n; j++ {
			v := none
			if hay[j] == c {
				if j >= 1 && prev[j-1] > none {
					v = prev[j-1] + bonusConsecutive + bonus[j]
				}
				if bestV > none && bestV+bonus[j] > v {
					v = bestV + bonus[j]
				}
			}
			cur[j] = v
			if j >= 1 && prev[j-1] > bestV {
				bestV = prev[j-1]
			}
		}
		prev, cur = cur, prev
	}
	s.prev, s.cur = prev, cur
	best := none
	for j := 0; j < n; j++ {
		if prev[j] > best {
			best = prev[j]
		}
	}
	return best
}

// term runs the dynamic programming for one term: dp[i][j] is the best
// score with q[i] at hay[j]. With from == nil only the score is kept, in
// O(n) memory; with from (m rows of n) the placement is recoverable.
func (s *scorer) term(q []rune, from [][]int) (int, int) {
	hay, bonus, prev, cur := s.hay, s.bonus, s.prev, s.cur
	n := len(hay)
	for i := range q {
		bestK, bestV := -1, none // best dp[i-1][k] for k < j-1
		for j := 0; j < n; j++ {
			cur[j] = none
			if from != nil {
				from[i][j] = -1
			}
			if hay[j] == q[i] {
				switch {
				case i == 0:
					cur[j] = bonus[j]
				default:
					if j >= 1 && prev[j-1] > none {
						cur[j] = prev[j-1] + bonusConsecutive + bonus[j]
						if from != nil {
							from[i][j] = j - 1
						}
					}
					if bestV > none && bestV+bonus[j] > cur[j] {
						cur[j] = bestV + bonus[j]
						if from != nil {
							from[i][j] = bestK
						}
					}
				}
			}
			if i > 0 && j >= 1 && prev[j-1] > bestV {
				bestK, bestV = j-1, prev[j-1]
			}
		}
		prev, cur = cur, prev
	}
	s.prev, s.cur = prev, cur
	endJ, endV := -1, none
	for j := 0; j < n; j++ {
		if prev[j] > endV {
			endJ, endV = j, prev[j]
		}
	}
	return endV, endJ
}

// positions is the best placement of every term, deduplicated, ascending.
func (s *scorer) positions(path string) []int {
	s.prepare(path)
	seen := map[int]bool{}
	var out []int
	for _, t := range s.q.terms {
		if !isSubsequence(t, s.hay) {
			return nil
		}
		from := make([][]int, len(t))
		for i := range from {
			from[i] = make([]int, len(s.hay))
		}
		_, j := s.term(t, from)
		for i := len(t) - 1; i >= 0; i-- {
			if !seen[j] {
				seen[j] = true
				out = append(out, j)
			}
			j = from[i][j]
		}
	}
	sort.Ints(out)
	return out
}

// Match reports whether query matches path, with the score and the
// highlighted positions. Spaces split the query into terms that must all
// match; the characters of a term must appear in the path in order. Case
// is ignored unless the query has an upper case letter.
func Match(query, path string) (Result, bool) {
	s := newScorer(query)
	v, ok := s.score(path)
	if !ok {
		return Result{}, false
	}
	return Result{Score: v, Positions: s.positions(path)}, true
}

// Highlight is the matched rune positions of query in path (nil when it
// does not match), for the rows a UI renders.
func Highlight(query, path string) []int {
	s := newScorer(query)
	if _, ok := s.score(path); !ok {
		return nil
	}
	return s.positions(path)
}

func isSubsequence(q, hay []rune) bool {
	i := 0
	for _, r := range hay {
		if i < len(q) && r == q[i] {
			i++
		}
	}
	return i == len(q)
}

func isBoundary(r rune) bool { return r == '/' || r == '.' || r == '_' || r == '-' }

// Rank is the paths that match query, best first; ties go to the shorter
// path, then alphabetically.
func Rank(query string, paths []string) []Hit {
	// Score in parallel chunks, one scorer (and its buffers) each.
	workers := min(runtime.GOMAXPROCS(0), max(1, len(paths)/2000))
	chunk := (len(paths) + workers - 1) / workers
	parts := make([][]Hit, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*chunk, min((w+1)*chunk, len(paths))
		if lo >= hi {
			continue
		}
		wg.Add(1)
		go func(w int, paths []string) {
			defer wg.Done()
			s := newScorer(query)
			hits := make([]Hit, 0, len(paths))
			for _, p := range paths {
				if v, ok := s.score(p); ok {
					hits = append(hits, Hit{Path: p, Score: v})
				}
			}
			parts[w] = hits
		}(w, paths[lo:hi])
	}
	wg.Wait()
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	hits := make([]Hit, 0, total)
	for _, p := range parts {
		hits = append(hits, p...)
	}
	// The order is total (the path is the last key), so no stable sort.
	slices.SortFunc(hits, func(a, b Hit) int {
		if a.Score != b.Score {
			return cmp.Compare(b.Score, a.Score)
		}
		if len(a.Path) != len(b.Path) {
			return cmp.Compare(len(a.Path), len(b.Path))
		}
		return strings.Compare(a.Path, b.Path)
	})
	return hits
}
