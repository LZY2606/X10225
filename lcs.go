package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a thin wrapper around the unified array matcher;
// there is a single LCS implementation shared with the
// production comparison path.
func lcs(src, tgt []interface{}) [][2]int {
	d := &Differ{}
	return d.newArrayMatcher(src, tgt, false).lcsPairs()
}
