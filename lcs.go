package jsondiff

// lcs computes the longest common subsequence of two slices and
// returns the index pairs of the LCS, that is, the indices into
// the source and target slices where the LCS items are located.
//
// It is a thin wrapper around [arrayMatcher], which means tests
// exercise the exact same equality, digest and LCS logic used by
// the production comparison path.
func lcs(src, tgt []interface{}) [][2]int {
	return newArrayMatcher(nil, src, tgt).lcsPairs()
}
