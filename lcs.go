package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
func lcs(src, tgt []interface{}) [][2]int {
	h := hasher{}
	digestOf := func(v interface{}) uint64 {
		return h.digest(v, false)
	}
	return newArrayMatcher(src, tgt, digestOf, nil).lcsPairs()
}
