package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a thin wrapper over the same unified array matcher
// used by the comparison pipeline: equality results are
// computed once and reused for backtracking.
func lcs(src, tgt []interface{}) [][2]int {
	return newArrayMatcher(newMatchCtx(), src, tgt).lcs()
}
