package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It delegates to the same arrayMatcher used by the
// production diff path: there is no second algorithm
// maintained in parallel.
func lcs(src, tgt []interface{}) [][2]int {
	pairs, _ := lcsPairs(src, tgt, nil)
	return pairs
}

// lcsPairs is the shared entry used by lcs and by legacy
// fallbacks. A non-nil stats argument collects diagnostics.
func lcsPairs(src, tgt []interface{}, stats *arrayDiffStats) ([][2]int, error) {
	var local arrayDiffStats
	if stats == nil {
		stats = &local
	}
	h := hasher{}
	m := newArrayMatcher(src, tgt, h.digest, stats)
	return m.lcs()
}

// lcsLegacy is the fallback LCS computation used when the
// unified matcher of a comparison declines an input. It
// shares the exact same implementation.
func lcsLegacy(src, tgt []interface{}, stats *arrayDiffStats) [][2]int {
	pairs, _ := lcsPairs(src, tgt, stats)
	return pairs
}
