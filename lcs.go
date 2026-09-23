package jsondiff

// lcs computes the longest common subsequence of two slices
// and returns the index pairs of the LCS, that is, the indices
// into the source and target slices where the LCS items are
// located.
//
// It is a thin wrapper over the unified array matcher used by
// the production path; it does not carry a second algorithm.
func lcs(src, tgt []interface{}) [][2]int {
	return lcsPairs(nil, src, tgt)
}
