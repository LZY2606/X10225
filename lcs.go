package jsondiff

// lcs computes the longest common subsequence of two slices
// and returns the index pairs of the LCS, that is, the
// indices into the source and target slices where the LCS
// items are located.
//
// It delegates to the same arrayMatcher implementation used
// by the production array diff path; there is no separate
// algorithm maintained for tests.
func lcs(src, tgt []interface{}) [][2]int {
	d := &Differ{}
	m := newArrayMatcher(d, src, tgt, nil)
	return m.lcsPairs()
}
