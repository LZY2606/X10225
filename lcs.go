package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
//
// It is a convenience wrapper around the arrayMatcher
// used by the Differ, so that both share the exact same
// matching logic.
func lcs(src, tgt []interface{}) [][2]int {
	d := Differ{}
	return d.newArrayMatcher(src, tgt).lcsPairs()
}
