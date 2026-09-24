package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a thin wrapper around the unified [arrayMatcher],
// so that standalone callers exercise the exact same
// equality, digest and memoization logic as the
// production array-diff path.
func lcs(src, tgt []interface{}) [][2]int {
	var d Differ
	return newArrayMatcher(&d, src, tgt, false).lcs()
}
