package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It delegates to lcsPairs, the single LCS implementation
// shared with the unified array matcher, using the naive
// deep-equal oracle. The matcher adds a hash prefilter and
// memoization on top of the same core, so pair selection is
// byte-for-byte identical to this function.
func lcs(src, tgt []interface{}) [][2]int {
	pairs, err := lcsPairs(len(src), len(tgt), func(i, j int) (bool, error) {
		return deepEqual(src[i], tgt[j]), nil
	})
	if err != nil {
		panic(err) // unreachable: the naive oracle never errors
	}
	return pairs
}
