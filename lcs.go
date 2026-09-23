package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It delegates to arrayMatcher, the production code path
// used by compareArraysLCS; there is no test-only
// implementation.
func lcs(src, tgt []interface{}) [][2]int {
	m := arrayMatcher{src: src, tgt: tgt}
	return m.lcs()
}
