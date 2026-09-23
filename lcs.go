package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
func lcs(src, tgt []interface{}) [][2]int {
	// The computation is delegated to an arrayMatcher so
	// that equality, hashing and the LCS share a single
	// implementation with the Differ comparison path.
	return newArrayMatcher(&hasher{}, src, tgt).lcs()
}
