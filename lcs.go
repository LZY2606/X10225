package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that is,
// the indices into the source and target slices where the
// LCS items are located.
//
// It is a thin wrapper around the production arrayMatcher:
// it shares the exact matching/equality implementation used
// by the diff generation, so the standalone tests exercise
// the real algorithm instead of a copy maintained for
// tests only.
func lcs(src, tgt []interface{}) [][2]int {
	var d Differ
	m := newArrayMatcher(&d, newEqOracle(&d), src, tgt, false)
	return m.lcsPairs()
}
