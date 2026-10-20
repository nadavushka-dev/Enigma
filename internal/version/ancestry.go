package version

import "sort"

// AncestryLimit is how many hashes nearest the current version a machine
// keeps and sends per file (SPEC §7 step 3).
const AncestryLimit = 64

// Node is one content hash and the hashes it was made from.
type Node struct {
	Hash    string   `json:"h"`
	Parents []string `json:"p,omitempty"`
}

// Graph maps a content hash to its parents. Hashes, not versions, so a
// revert can make it loop.
type Graph map[string][]string

// Add merges nodes into g; a hash reached by several paths keeps the union
// of its parents.
func (g Graph) Add(nodes []Node) {
	for _, n := range nodes {
		set := map[string]bool{}
		for _, p := range g[n.Hash] {
			set[p] = true
		}
		for _, p := range n.Parents {
			set[p] = true
		}
		if len(set) == 0 {
			if _, ok := g[n.Hash]; !ok {
				g[n.Hash] = nil
			}
			continue
		}
		ps := make([]string, 0, len(set))
		for p := range set {
			ps = append(ps, p)
		}
		sort.Strings(ps)
		g[n.Hash] = ps
	}
}

// Walk lists the ancestry of start, breadth first, at most limit hashes.
func Walk(g Graph, start string, limit int) []Node {
	var out []Node
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 && len(out) < limit {
		h := queue[0]
		queue = queue[1:]
		ps := g[h]
		out = append(out, Node{Hash: h, Parents: ps})
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				queue = append(queue, p)
			}
		}
	}
	return out
}

// Trim returns the part of g within limit hashes of start.
func Trim(g Graph, start string, limit int) Graph {
	out := Graph{}
	out.Add(Walk(g, start, limit))
	return out
}

// distances: steps from the list's first hash, following parents within the list.
func distances(nodes []Node) map[string]int {
	if len(nodes) == 0 {
		return nil
	}
	g := Graph{}
	g.Add(nodes)
	d := map[string]int{nodes[0].Hash: 0}
	queue := []string{nodes[0].Hash}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		for _, p := range g[h] {
			if _, ok := d[p]; !ok {
				d[p] = d[h] + 1
				queue = append(queue, p)
			}
		}
	}
	return d
}

// CommonBase picks the merge base of two ancestries: among hashes in both,
// the closest to both (smallest larger distance, then smallest sum, then
// lowest hash). It is symmetric, so two machines merging the same pair pick
// the same base.
func CommonBase(a, b []Node) (string, bool) {
	da, db := distances(a), distances(b)
	best, bestMax, bestSum, found := "", 0, 0, false
	for h, x := range da {
		y, ok := db[h]
		if !ok || h == Deleted {
			continue
		}
		m, s := max(x, y), x+y
		if !found || m < bestMax || (m == bestMax && (s < bestSum || (s == bestSum && h < best))) {
			best, bestMax, bestSum, found = h, m, s, true
		}
	}
	return best, found
}
