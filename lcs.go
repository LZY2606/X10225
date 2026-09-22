package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a thin wrapper over arrayMatcher, the single
// implementation shared with the production diff path:
// equality is resolved once per old/new pair through the
// memoized matcher instead of being recomputed during
// backtracking.
func lcs(src, tgt []interface{}) [][2]int {
	d := &Differ{}
	return d.newArrayMatcher(src, tgt).lcsPairs()
}
