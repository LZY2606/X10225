package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a thin wrapper around the matcher-backed
// implementation so that there is a single LCS algorithm:
// the production path and standalone callers exercise the
// exact same code, including per-pair memoization.
func lcs(src, tgt []interface{}) [][2]int {
	return (&arrayMatcher{}).lcs(src, tgt)
}
