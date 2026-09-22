package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
func lcs(src, tgt []interface{}) [][2]int {
	return lcsMatch(src, tgt, func(i, j int) bool {
		return deepEqual(src[i], tgt[j])
	})
}

// lcsMatch is the generic version of lcs. It uses the
// given equality function to compare the elements of the
// source and target slices by their indices, which allows
// the caller to compute the equality relation only once,
// and to account for the comparisons performed.
func lcsMatch(src, tgt []interface{}, eq func(i, j int) bool) [][2]int {
	t := make([][]int, len(src)+1)

	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if eq(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := len(src), len(tgt)
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case eq(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t[i-1][j] > t[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}
