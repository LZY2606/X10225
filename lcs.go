package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// It is a thin adapter over the shared, memoized array
// matching pipeline: using it directly keeps a single LCS
// implementation instead of maintaining a parallel one.
func lcs(src, tgt []interface{}) [][2]int {
	d := &Differ{}
	d.matchCounters = matchCounters{}
	d.matcher = newArrayMatcher(d)
	d.matcher.beginArray(src, tgt)
	return d.matcher.lcsPairs(src, tgt)
}
