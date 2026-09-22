package jsondiff

// lcs computes the longest common subsequence of two slices
// and returns the index pairs of the LCS, that is, the indices
// into the source and target slices where the LCS items are
// located.
//
// It is a thin wrapper around the production matching logic of
// arrayMatcher, which owns the equality/hashing responsibilities.
func lcs(src, tgt []interface{}) [][2]int {
	var stats compareStats
	m := arrayMatcher{
		digest: func(any, bool) uint64 { return 0 },
		stats:  &stats,
	}
	return m.lcsPairs(src, tgt)
}
