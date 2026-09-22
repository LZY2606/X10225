package jsondiff

// lcs computes the longest common subsequence of two slices
// and returns the index pairs of the LCS, that is, the indices
// into the source and target slices where the LCS items are
// located.
//
// It delegates to the same unified array matcher used by the
// production diff paths: equality, hashing and the memoized
// predicate are not duplicated for tests or internal callers.
func lcs(src, tgt []interface{}) [][2]int {
	m := (&Differ{}).newStandaloneMatcher(src, tgt)
	return m.lcsPairs()
}
