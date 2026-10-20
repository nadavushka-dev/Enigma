package merge

// LineChanges reports, for each line of a and of b, whether it is not part
// of a longest common subsequence of the two: the lines a viewer should mark
// as differing. Lines are split as split does (each keeps its newline). Nil
// is returned for an empty input. If the texts differ by more than the diff
// bound, every line is marked.
func LineChanges(a, b []byte) (aChanged, bChanged []bool) {
	la, lb := split(a), split(b)
	if len(la) > 0 {
		aChanged = make([]bool, len(la))
	}
	if len(lb) > 0 {
		bChanged = make([]bool, len(lb))
	}
	hs, ok := diff(la, lb)
	if !ok {
		for i := range aChanged {
			aChanged[i] = true
		}
		for i := range bChanged {
			bChanged[i] = true
		}
		return aChanged, bChanged
	}
	for _, h := range hs {
		for i := h.bs; i < h.be; i++ {
			aChanged[i] = true
		}
		for i := h.os; i < h.oe; i++ {
			bChanged[i] = true
		}
	}
	return aChanged, bChanged
}
