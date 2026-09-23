package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It delegates to an arrayMatcher so that equality,
// hashing and LCS construction share a single, memoized
// implementation.
func lcs(src, tgt []interface{}) [][2]int {
	d := &Differ{}
	m := newArrayMatcher(d, src, tgt, false)
	return m.lcs()
}
