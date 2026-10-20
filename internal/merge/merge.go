// Package merge is the three-way line merge of concurrent text edits
// (SPEC §7 step 3).
package merge

import (
	"bytes"
	"sort"
	"unicode/utf8"
)

// textWindow is how much of a file text detection looks at (SPEC §7).
const textWindow = 8 << 10

// maxEdits bounds the diff; files that differ more are not merged.
const maxEdits = 1000

// IsText: valid UTF-8 and no NUL bytes in the first 8 KB.
func IsText(b []byte) bool {
	w := b[:min(len(b), textWindow)]
	if bytes.IndexByte(w, 0) >= 0 {
		return false
	}
	if len(w) < len(b) { // the window may cut the last rune
		for i := 0; i < utf8.UTFMax && len(w) > 0 && !utf8.FullRune(w[lastRuneStart(w):]); i++ {
			w = w[:lastRuneStart(w)]
		}
	}
	return utf8.Valid(w)
}

func lastRuneStart(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			return i
		}
	}
	return len(b)
}

// Merge3 merges a and b, two edits of base. ok is false when the edits
// overlap or touch. The result does not depend on which side is a: two
// machines merging the same pair make the same bytes.
func Merge3(base, a, b []byte) ([]byte, bool) {
	bl, al, cl := split(base), split(a), split(b)
	ha, okA := diff(bl, al)
	hb, okB := diff(bl, cl)
	if !okA || !okB {
		return nil, false
	}
	all := make([]hunk, 0, len(ha)+len(hb))
	for _, h := range ha {
		h.side = 0
		all = append(all, h)
	}
	for _, h := range hb {
		h.side = 1
		all = append(all, h)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].bs != all[j].bs {
			return all[i].bs < all[j].bs
		}
		return all[i].be < all[j].be
	})
	sides := [2][]string{al, cl}
	var out bytes.Buffer
	pos := 0
	for i := 0; i < len(all); {
		s, e := all[i].bs, all[i].be
		var in [2][]hunk
		j := i
		for ; j < len(all) && all[j].bs <= e; j++ { // overlapping or touching
			e = max(e, all[j].be)
			in[all[j].side] = append(in[all[j].side], all[j])
		}
		i = j
		writeLines(&out, bl[pos:s])
		ra := regionText(bl, sides[0], in[0], s, e)
		rb := regionText(bl, sides[1], in[1], s, e)
		switch {
		case len(in[1]) == 0:
			out.WriteString(ra)
		case len(in[0]) == 0, ra == rb:
			out.WriteString(rb)
		default:
			return nil, false
		}
		pos = e
	}
	writeLines(&out, bl[pos:])
	return out.Bytes(), true
}

// regionText is one side's text for base lines [s,e), given its hunks there.
func regionText(base, side []string, hs []hunk, s, e int) string {
	var out bytes.Buffer
	pos := s
	for _, h := range hs {
		writeLines(&out, base[pos:h.bs])
		writeLines(&out, side[h.os:h.oe])
		pos = h.be
	}
	writeLines(&out, base[pos:e])
	return out.String()
}

func writeLines(w *bytes.Buffer, ls []string) {
	for _, l := range ls {
		w.WriteString(l)
	}
}

// split cuts b into lines, each keeping its terminator; a last line without
// one is a different line from the same text with one.
func split(b []byte) []string {
	var out []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, string(b))
			break
		}
		out = append(out, string(b[:i+1]))
		b = b[i+1:]
	}
	return out
}

// hunk: base lines [bs,be) became other lines [os,oe).
type hunk struct {
	bs, be, os, oe int
	side           int
}

// diff returns the hunks turning a into b (Myers, after trimming the common
// prefix and suffix). ok is false if they differ by more than maxEdits.
func diff(a, b []string) ([]hunk, bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma) == 0 && len(mb) == 0 {
		return nil, true
	}
	// Lines as ints, for fast comparison.
	ids := map[string]int{}
	id := func(ls []string) []int {
		out := make([]int, len(ls))
		for i, l := range ls {
			v, ok := ids[l]
			if !ok {
				v = len(ids)
				ids[l] = v
			}
			out[i] = v
		}
		return out
	}
	x, y := id(ma), id(mb)
	matches, ok := myers(x, y)
	if !ok {
		return nil, false
	}
	// Hunks are the gaps between matched lines.
	var hs []hunk
	i, j := 0, 0
	for _, m := range append(matches, [2]int{len(x), len(y)}) {
		if m[0] > i || m[1] > j {
			hs = append(hs, hunk{bs: pre + i, be: pre + m[0], os: pre + j, oe: pre + m[1]})
		}
		i, j = m[0]+1, m[1]+1
	}
	return hs, true
}

// myers returns the matched (x index, y index) pairs of a shortest edit
// script, in order.
func myers(x, y []int) ([][2]int, bool) {
	n, m := len(x), len(y)
	maxD := min(n+m, maxEdits)
	off := maxD + 1
	v := make([]int, 2*maxD+3)
	var trace [][]int
	for d := 0; d <= maxD; d++ {
		// Only diagonals -d..d are read back: keep just those (memory ~ D²).
		snap := make([]int, 2*d+1)
		copy(snap, v[off-d:off+d+1])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var i int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				i = v[off+k+1] // down: insertion
			} else {
				i = v[off+k-1] + 1 // right: deletion
			}
			j := i - k
			for i < n && j < m && x[i] == y[j] {
				i, j = i+1, j+1
			}
			v[off+k] = i
			if i >= n && j >= m {
				return backtrack(trace, x, y, d, k), true
			}
		}
	}
	return nil, false
}

func backtrack(trace [][]int, x, y []int, d, k int) [][2]int {
	var out [][2]int
	snake := func(fromX, fromY, toX, toY int) { // diagonal run, walked backwards
		for toX > fromX && toY > fromY {
			toX, toY = toX-1, toY-1
			out = append(out, [2]int{toX, toY})
		}
	}
	cx, cy := len(x), len(y)
	for ; d > 0; d-- {
		snap := trace[d] // the furthest points after d-1 edits
		v := func(k int) int { return snap[k+d] }
		pk := k - 1 // came right: x[px] deleted
		if k == -d || (k != d && v(k-1) < v(k+1)) {
			pk = k + 1 // came down: y[py] inserted
		}
		px := v(pk)
		py := px - pk
		ax, ay := px+1, py // after the edit
		if pk == k+1 {
			ax, ay = px, py+1
		}
		snake(ax, ay, cx, cy)
		cx, cy, k = px, py, pk
	}
	snake(0, 0, cx, cy)
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}
