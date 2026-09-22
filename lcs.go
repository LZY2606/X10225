package jsondiff

// lcs computes the longest common subsequence of two slices and
// returns the index pairs of the LCS, that is, the indices into the
// source and target slices where the LCS items are located.
//
// It is a thin wrapper over the production arrayMatcher: the DP
// table, memoized equality and backtracking all live in matcher.go,
// so callers that do not need the surrounding diagnostics reuse the
// exact same algorithm instead of a duplicated one.
func lcs(src, tgt []interface{}) [][2]int {
	return newArrayMatcher(src, tgt).lcs()
}
