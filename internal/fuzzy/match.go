// Package fuzzy finds paths by a few typed characters (SPEC §12.4): a
// matcher that scores and highlights, and a background index of ~.
// Standard library only.
package fuzzy

import (
	"sort"
	"strings"
	"unicode"
)

// Result is one match: its score (higher is better) and the matched rune
// positions in the path, ascending, for highlighting.
type Result struct {
	Score     int
	Positions []int
}

// Hit is a ranked path.
type Hit struct {
	Path string
	Result
}

const (
	bonusConsecutive  = 3 // right after the previous matched character
	bonusSegmentStart = 3 // at the start of a segment (after / . _ -)
	bonusBaseName     = 1 // in the base name
)

// Match reports whether query matches path. Spaces split the query into
// terms that must all match; the characters of a term must appear in the
// path in order. Case is ignored unless the query has an upper case letter.
func Match(query, path string) (Result, bool) {
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return Result{}, true
	}
	sensitive := strings.IndexFunc(query, unicode.IsUpper) >= 0
	hay := []rune(path)
	if !sensitive {
		hay = []rune(strings.ToLower(path))
	}
	var total Result
	seen := map[int]bool{}
	for _, t := range terms {
		r, ok := matchTerm([]rune(t), hay, path)
		if !ok {
			return Result{}, false
		}
		total.Score += r.Score
		for _, p := range r.Positions {
			if !seen[p] {
				seen[p] = true
				total.Positions = append(total.Positions, p)
			}
		}
	}
	sort.Ints(total.Positions)
	return total, true
}

// matchTerm finds the best placement of q in hay (q is a subsequence of
// hay) by dynamic programming: dp[i][j] is the best score with q[i] at
// hay[j]. orig is the path as written, for the segment starts.
func matchTerm(q, hay []rune, orig string) (Result, bool) {
	n, m := len(hay), len(q)
	if m == 0 || !isSubsequence(q, hay) {
		return Result{}, m == 0
	}
	bonus := make([]int, n)
	base := strings.LastIndex(orig, "/") + 1 // rune index: paths are compared by rune below
	baseRunes := len([]rune(orig[:base]))
	for j := range hay {
		if j == 0 || isBoundary(hay[j-1]) {
			bonus[j] += bonusSegmentStart
		}
		if j >= baseRunes {
			bonus[j] += bonusBaseName
		}
	}
	const none = -1 << 30
	prev := make([]int, n) // dp[i-1][*]
	from := make([][]int, m)
	for i := range from {
		from[i] = make([]int, n)
	}
	cur := make([]int, n)
	for i := 0; i < m; i++ {
		for j := range cur {
			cur[j] = none
			from[i][j] = -1
		}
		bestK, bestV := -1, none // best dp[i-1][k] for k < j-1
		for j := 0; j < n; j++ {
			if hay[j] != q[i] {
				if i > 0 && j >= 1 && prev[j-1] > bestV {
					bestK, bestV = j-1, prev[j-1]
				}
				continue
			}
			if i == 0 {
				cur[j] = bonus[j]
			} else {
				if j >= 1 && prev[j-1] > none {
					cur[j] = prev[j-1] + bonusConsecutive + bonus[j]
					from[i][j] = j - 1
				}
				if bestV > none && bestV+bonus[j] > cur[j] {
					cur[j] = bestV + bonus[j]
					from[i][j] = bestK
				}
			}
			if i > 0 && j >= 1 && prev[j-1] > bestV {
				bestK, bestV = j-1, prev[j-1]
			}
		}
		prev, cur = cur, prev
	}
	endJ, endV := -1, none
	for j := 0; j < n; j++ {
		if prev[j] > endV {
			endJ, endV = j, prev[j]
		}
	}
	pos := make([]int, m)
	for i, j := m-1, endJ; i >= 0; i-- {
		pos[i] = j
		j = from[i][j]
	}
	return Result{Score: endV, Positions: pos}, true
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
	var hits []Hit
	for _, p := range paths {
		if r, ok := Match(query, p); ok {
			hits = append(hits, Hit{Path: p, Result: r})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if len(a.Path) != len(b.Path) {
			return len(a.Path) < len(b.Path)
		}
		return a.Path < b.Path
	})
	return hits
}
