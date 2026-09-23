package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a convenience wrapper around the Differ.lcsPairs
// method, which carries the production logic, so that the
// matching can be exercised without a full comparison.
func lcs(src, tgt []interface{}) [][2]int {
	var d Differ
	return d.lcsPairs(src, tgt)
}

// lcsPairs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS.
//
// The digest of each element is computed exactly once, and
// is used to select the candidate pairs; a deep equality
// check confirms each candidate, which guarantees that
// distinct values are never paired by a hash collision.
// The number of digests and equality checks is accounted
// for in the diagnostic counters of the Differ.
func (d *Differ) lcsPairs(src, tgt []interface{}) [][2]int {
	// Compute the digest of each element once.
	srcDigests := make([]uint64, len(src))
	tgtDigests := make([]uint64, len(tgt))
	for i, v := range src {
		srcDigests[i] = d.digestOf(v, false)
	}
	for j, v := range tgt {
		tgtDigests[j] = d.digestOf(v, false)
	}
	// equal reports whether the elements at indices i and j
	// of the source and target slices are equal. Pairs whose
	// digests differ are rejected without a deep equality
	// check; pairs whose digests match are confirmed.
	equal := func(i, j int) bool {
		if srcDigests[i] != tgtDigests[j] {
			return false
		}
		return d.confirmEquality(src[i], tgt[j])
	}

	t := make([][]int, len(src)+1)

	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := len(src), len(tgt)
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case equal(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t[i-1][j] > t[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}
