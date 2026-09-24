package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// This is a thin wrapper over the unified array matcher;
// keeping the function preserves the historical
// package-internal entry point while ensuring a single
// LCS/equality implementation is maintained.
func lcs(src, tgt []interface{}) [][2]int {
	d := Differ{}
	m := d.newArrayMatcher(src, tgt, false)
	return m.lcs()
}
