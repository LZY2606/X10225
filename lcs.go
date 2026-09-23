package jsondiff

// lcs computes the longest common subsequence of two
// slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices
// where the LCS items are located
func lcs(src, tgt []interface{}) [][2]int {
	d := Differ{}
	return d.lcs(src, tgt)
}

// lcs is the Differ-bound variant of the top-level lcs
// function, which records diagnostic counters about the
// comparisons performed while matching the elements.
func (d *Differ) lcs(src, tgt []interface{}) [][2]int {
	return newPairMatcher(d, src, tgt).pairs()
}

// A pairMatcher unifies the responsibilities of equality
// testing and hashing for the elements of two slices being
// matched. The digest of each element is computed exactly
// once, up front, and serves as a pre-filter: pairs with
// distinct digests are never equal, while pairs with equal
// digests are confirmed with a deep comparison, at most
// once per pair. This guarantees that duplicate values are
// never mispaired by a hash collision, and bounds the
// number of deep comparisons to the number of candidate
// pairs that share a digest.
type pairMatcher struct {
	d         *Differ
	src, tgt  []interface{}
	srcDigest []uint64
	tgtDigest []uint64
	memo      map[[2]int]bool
}

func newPairMatcher(d *Differ, src, tgt []interface{}) *pairMatcher {
	pm := &pairMatcher{
		d:         d,
		src:       src,
		tgt:       tgt,
		srcDigest: make([]uint64, len(src)),
		tgtDigest: make([]uint64, len(tgt)),
	}
	for i, v := range src {
		validateJSONValue(v)
		pm.srcDigest[i] = d.digest(v, false)
	}
	for j, v := range tgt {
		validateJSONValue(v)
		pm.tgtDigest[j] = d.digest(v, false)
	}
	return pm
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// The result is memoized so that each candidate pair is
// compared at most once, whether it is consulted during the
// fill or the backtrack phase of the LCS computation.
func (pm *pairMatcher) equal(i, j int) bool {
	if pm.srcDigest[i] != pm.tgtDigest[j] {
		return false
	}
	key := [2]int{i, j}
	if eq, ok := pm.memo[key]; ok {
		return eq
	}
	eq := pm.d.matchEqual(pm.src[i], pm.tgt[j])
	if pm.memo == nil {
		pm.memo = make(map[[2]int]bool)
	}
	pm.memo[key] = eq
	return eq
}

// pairs computes the index pairs of the longest common
// subsequence of the source and target slices.
func (pm *pairMatcher) pairs() [][2]int {
	src, tgt := pm.src, pm.tgt

	t := make([][]int, len(src)+1)
	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if pm.equal(i-1, j-1) {
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
		case pm.equal(i-1, j-1):
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
