package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
func lcs(src, tgt []interface{}) [][2]int {
	// The LCS logic lives in arrayMatcher so that hashing,
	// equality and the longest common subsequence share a
	// single implementation with the production comparison
	// path; this wrapper exists for callers that do not run
	// within a full comparison.
	return (&Differ{}).newArrayMatcher(src, tgt, false).lcs()
}
