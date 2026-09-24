package jsondiff

// arrayMatcher unifies the four responsibilities that
// used to be scattered across the array-diff code path:
//
//   - equality between elements of the source and target
//     arrays,
//   - digest computation, computed once per element,
//   - longest common subsequence matching, and
//   - move/copy candidate discovery.
//
// Digests act as a fast negative filter only. Any pair of
// elements whose digests agree is verified with deep
// equality, and positive results are memoized by index
// pair, so that each pair is compared at most once and
// digest collisions can never cause a wrong match.
//
// An arrayMatcher is created per array comparison and
// holds no cross-comparison state; concurrent top-level
// comparisons therefore remain fully isolated.
type arrayMatcher struct {
	src, tgt []interface{}
	sh, th   []uint64
	hasher   *hasher
	stats    *compareStats
	sort     bool
	eqMemo   map[[2]int]bool
}

// newArrayMatcher builds the matcher for the given arrays.
// Every element of both arrays is digested exactly once.
// When sort is true, array elements are hashed as
// unordered collections (the digest used by the Equivalent
// option); the flag must be false for LCS matching, which
// is order sensitive.
func newArrayMatcher(d *Differ, src, tgt []interface{}, sort bool) *arrayMatcher {
	m := &arrayMatcher{
		src:    src,
		tgt:    tgt,
		sh:     make([]uint64, len(src)),
		th:     make([]uint64, len(tgt)),
		hasher: &d.hasher,
		stats:  &d.compareStats,
		sort:   sort,
	}
	for i := range src {
		m.sh[i] = d.digest(src[i], sort)
	}
	for i := range tgt {
		m.th[i] = d.digest(tgt[i], sort)
	}
	return m
}

// equal reports whether src[i] and tgt[j] are deeply
// equal. Digest mismatch short-circuits the comparison;
// otherwise the result is computed once with deep
// equality and memoized, so LCS backtracking and move
// discovery share the exact same comparison.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.sh[i] != m.th[j] {
		return false
	}
	if m.eqMemo == nil {
		m.eqMemo = make(map[[2]int]bool)
	}
	key := [2]int{i, j}
	if eq, ok := m.eqMemo[key]; ok {
		return eq
	}
	if m.stats != nil {
		m.stats.equalityComparisons++
	}
	eq := deepEqual(m.src[i], m.tgt[j])
	m.eqMemo[key] = eq
	return eq
}

// lcs returns the index pairs of the longest common
// subsequence of the two arrays. Equality is resolved by
// the matcher, meaning each element pair is compared at
// most once for both the DP fill and the backtrack.
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

// unorderedEqual reports whether both arrays contain the
// same multiset of elements, ignoring their order. The
// multiplicity of every distinct value is honored, and
// values grouped under the same digest are always
// compared for equality, so duplicate elements and digest
// collisions cannot be confused.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	type group struct {
		rep int
		n   int
	}
	groups := make(map[uint64][]group, len(m.src))

	for i := range m.src {
		bucket := groups[m.sh[i]]
		g := -1
		for k := range bucket {
			if m.equalValues(bucket[k].rep, i, m.sh[bucket[k].rep], m.sh[i], m.src) {
				g = k
				break
			}
		}
		if g < 0 {
			bucket = append(bucket, group{rep: i, n: 1})
		} else {
			bucket[g].n++
		}
		groups[m.sh[i]] = bucket
	}
	for j := range m.tgt {
		bucket := groups[m.th[j]]
		g := -1
		for k := range bucket {
			if bucket[k].n > 0 && m.equalValue(bucket[k].rep, j, m.src, m.tgt) {
				g = k
				break
			}
		}
		if g < 0 {
			return false
		}
		bucket[g].n--
	}
	for _, bucket := range groups {
		for _, g := range bucket {
			if g.n != 0 {
				return false
			}
		}
	}
	return true
}

// equalValues compares two elements that both belong to
// the same array, and equalValue compares a source element
// with a target element. The memo used by the latter is
// the same one used by LCS matching.
func (m *arrayMatcher) equalValues(ai, bi int, ha, hb uint64, arr []interface{}) bool {
	if ha != hb {
		return false
	}
	if m.stats != nil {
		m.stats.equalityComparisons++
	}
	return deepEqual(arr[ai], arr[bi])
}

func (m *arrayMatcher) equalValue(si, ti int, src, tgt []interface{}) bool {
	return m.equal(si, ti)
}
