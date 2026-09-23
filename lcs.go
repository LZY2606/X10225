package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
func lcs(src, tgt []interface{}) [][2]int {
	return lcsPairs(src, tgt, deepEqual)
}

// lcsPairs computes the index pairs of the longest common
// subsequence of the src and tgt slices, using the given
// equality function to compare their elements.
//
// The equality of a pair of elements is evaluated at most
// once: the results are recorded in a matrix that is shared
// by the dynamic programming table and the backtracking
// phase, so that the number of comparisons performed is
// exactly len(src)*len(tgt).
func lcsPairs(src, tgt []interface{}, equal func(a, b interface{}) bool) [][2]int {
	sl, tl := len(src), len(tgt)

	// Record the equality of each pair of elements once,
	// and share the results between the two phases of the
	// algorithm.
	eqm := make([]bool, sl*tl)
	for i := 0; i < sl; i++ {
		for j := 0; j < tl; j++ {
			eqm[i*tl+j] = equal(src[i], tgt[j])
		}
	}
	t := make([][]int, sl+1)
	for i := 0; i <= sl; i++ {
		t[i] = make([]int, tl+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if eqm[(i-1)*tl+(j-1)] {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := sl, tl
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case eqm[(i-1)*tl+(j-1)]:
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
