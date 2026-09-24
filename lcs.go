package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
func lcs(src, tgt []interface{}) [][2]int {
	h := hasher{}
	m := newArrayMatcher(src, tgt, func(v interface{}, sort bool) uint64 {
		return h.digest(v, sort)
	}, nil)
	return m.lcs()
}
