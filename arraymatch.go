package jsondiff

// Stats contains diagnostic counters collected by a Differ
// during the last comparison. The counters are strictly
// diagnostic: they have no effect on the generated patch,
// and each Differ instance accumulates its own counters, so
// concurrent comparisons performed by distinct instances (or
// through the package-level Compare functions, which always
// use a fresh Differ) are isolated from one another.
type Stats struct {
	// EqualComparisons is the number of deep-equality
	// comparisons performed while matching array elements
	// and discovering move/copy candidates.
	EqualComparisons int
	// HashCollisions is the number of candidate pairs whose
	// digests matched but that deep-equality rejected. It
	// proves that colliding hashes are never paired without
	// a deep-equality verification.
	HashCollisions int
}

// An arrayMatcher unifies the concerns of element equality,
// hashing, longest common subsequence computation and
// move/copy candidate discovery for a single array
// comparison. The digest of each element is computed exactly
// once and reused to gate deep-equality checks, and the
// result of each check is memoized so that any candidate
// pair is compared at most once. The number of deep-equality
// comparisons performed is reported through the Stats of the
// Differ that owns the matcher.
type arrayMatcher struct {
	src, tgt    []interface{}
	srcHash     []uint64
	tgtHash     []uint64
	eq          map[[2]int]bool
	comparisons int
	collisions  int
}

// newArrayMatcher returns an arrayMatcher for the given
// slices, computing the digest of every element once.
func newArrayMatcher(h *hasher, src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		src:     src,
		tgt:     tgt,
		srcHash: make([]uint64, len(src)),
		tgtHash: make([]uint64, len(tgt)),
	}
	for i, v := range src {
		m.srcHash[i] = h.digest(v, false)
	}
	for j, v := range tgt {
		m.tgtHash[j] = h.digest(v, false)
	}
	return m
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// Elements with different digests cannot be equal, while
// elements with matching digests are verified by deepEqual
// exactly once, so that duplicate values are never paired
// incorrectly because of a hash collision.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.srcHash[i] != m.tgtHash[j] {
		return false
	}
	k := [2]int{i, j}
	if eq, ok := m.eq[k]; ok {
		return eq
	}
	eq := deepEqual(m.src[i], m.tgt[j])
	m.comparisons++
	if !eq {
		m.collisions++
	}
	if m.eq == nil {
		m.eq = make(map[[2]int]bool)
	}
	m.eq[k] = eq
	return eq
}

// lcs computes the longest common subsequence of the two
// slices held by the matcher and returns the index pairs of
// the LCS, that is, the indices into the source and target
// slices where the LCS items are located.
func (m *arrayMatcher) lcs() [][2]int {
	src, tgt := m.src, m.tgt
	t := make([][]int, len(src)+1)

	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if m.equal(i-1, j-1) {
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
		case m.equal(i-1, j-1):
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
