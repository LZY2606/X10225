package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It delegates to the matcher so that the standalone helper
// and the production array comparison share a single
// implementation: equality is memoized and hash-pre-filtered
// instead of being re-evaluated for every DP cell and again
// during backtracking.
func lcs(src, tgt []interface{}) [][2]int {
	return newMatcher(matchHooks{}).lcs(src, tgt)
}
