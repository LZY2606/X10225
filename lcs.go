package jsondiff

// lcs computes the longest common subsequence of two slices and
// returns the index pairs of the LCS, that is, the indices into
// the source and target slices where the LCS items are located.
//
// The actual matching logic lives in [arrayMatcher], so that the
// LCS and the array diff production path share a single
// implementation of equality, hashing and candidate evaluation.
func lcs(src, tgt []interface{}) [][2]int {
	return newArrayMatcher(src, tgt, false, &diffStats{}).lcs()
}
