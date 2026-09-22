package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It delegates to a matcher that computes the digest of
// each element once and confirms candidate pairs with a
// deep-equality comparison.
func lcs(src, tgt []interface{}) [][2]int {
	m := matcher{
		hasher: &hasher{},
		stats:  &diffStats{},
	}
	return m.lcs(src, tgt)
}
