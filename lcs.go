package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located.
func lcs(src, tgt []interface{}) [][2]int {
	return computeLCS(src, tgt, func(i, j int) bool {
		return deepEqual(src[i], tgt[j])
	})
}

// lcsPairs is the production counterpart of lcs. It
// routes every equality evaluation through the Differ
// matcher, so LCS table cells are counted once for
// diagnostics and each old/new pair is evaluated at most
// once even though the backtracking pass revisits cells.
func (d *Differ) lcsPairs(scope string, src, tgt []interface{}) [][2]int {
	return computeLCS(src, tgt, func(i, j int) bool {
		return d.matchIndexEqual(stageLCS, eqLCSKey{
			scope:  scope,
			srcIdx: i,
			tgtIdx: j,
		}, src[i], tgt[j])
	})
}

// computeLCS contains the unique dynamic-programming
// implementation of the LCS. The equal predicate is
// supplied by the caller: tests and the package-level
// helper use direct equality, while production diffs use
// the counting/memoizing matcher. No matching algorithm is
// duplicated for test purposes.
func computeLCS(src, tgt []interface{}, equal func(i, j int) bool) [][2]int {
	t := make([][]int, len(src)+1)

	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if equal(i-1, j-1) {
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
		case equal(i-1, j-1):
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
