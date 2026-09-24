package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
//
// The equality relation between the elements of both
// slices is evaluated by an arrayMatcher, which computes
// each candidate at most once; see match.go.
func lcs(src, tgt []interface{}) [][2]int {
	h := hasher{}
	return newArrayMatcher(src, tgt, &h, nil).pairs()
}
