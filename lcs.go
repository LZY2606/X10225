package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a thin wrapper over arrayMatcher, which unifies
// hashing, equality and LCS computation with the rest of
// the array matching pipeline.
func lcs(src, tgt []interface{}) [][2]int {
	return newArrayMatcher(nil, nil, src, tgt).lcsPairs()
}
