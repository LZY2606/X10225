package jsondiff

// arrayMatcher owns the four responsibilities that cooperate
// while matching two JSON arrays:
//
//   - equality, memoized per (src,tgt) index pair so that a
//     candidate pair is deep-compared at most once;
//   - hashing, computed lazily once per element and used only as
//     a prefilter for equality (a hash collision can never match
//     two distinct values);
//   - the longest common subsequence (LCS), built on top of the
//     memoized equality table;
//   - move/copy candidate discovery, served from the same table.
//
// A matcher is scoped to a single array comparison and shares its
// equality logic with the production differ: it never re-implements
// equality, it only caches calls to deepEqual.
type arrayMatcher struct {
	d          *Differ
	src, tgt   []interface{}
	equivalent bool

	// Per-element digests, materialized lazily. zero value of a
	// slot is ambiguous, hence the parallel known slice.
	srcHash  []uint64
	tgtHash  []uint64
	srcKnown []bool
	tgtKnown []bool

	// memo holds the result of every equality probe, addressed
	// as memo[i*len(tgt)+j]. Values are tri-state via memKnown.
	memo     []bool
	memKnown []bool
}

func newArrayMatcher(d *Differ, src, tgt []interface{}, equivalent bool) *arrayMatcher {
	m := &arrayMatcher{
		d:          d,
		src:        src,
		tgt:        tgt,
		equivalent: equivalent,
	}
	if n := len(src); n > 0 {
		m.srcHash = make([]uint64, n)
		m.srcKnown = make([]bool, n)
	}
	if n := len(tgt); n > 0 {
		m.tgtHash = make([]uint64, n)
		m.tgtKnown = make([]bool, n)
	}
	if a, b := len(src), len(tgt); a > 0 && b > 0 {
		m.memo = make([]bool, a*b)
		m.memKnown = make([]bool, a*b)
	}
	return m
}

// digestSrc and digestTgt return the digest of a source/target
// element, computing it exactly once per element.
func (m *arrayMatcher) digestSrc(i int) uint64 {
	if !m.srcKnown[i] {
		m.srcHash[i] = m.d.digest(m.src[i], m.equivalent)
		m.srcKnown[i] = true
	}
	return m.srcHash[i]
}

func (m *arrayMatcher) digestTgt(j int) uint64 {
	if !m.tgtKnown[j] {
		m.tgtHash[j] = m.d.digest(m.tgt[j], m.equivalent)
		m.tgtKnown[j] = true
	}
	return m.tgtHash[j]
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// Distinct hashes settle the pair without a deep comparison;
// equal hashes are always confirmed with deepEqual, so a hash
// collision can never pair distinct elements. Every pair is
// settled at most once.
func (m *arrayMatcher) equal(i, j int) bool {
	k := i*len(m.tgt) + j
	if m.memKnown != nil && m.memKnown[k] {
		return m.memo[k]
	}
	eq := false
	if m.digestSrc(i) == m.digestTgt(j) {
		m.d.stats.EqualDeep++
		eq = matcherDeepEqual(m.src[i], m.tgt[j])
	}
	if m.memKnown != nil {
		m.memKnown[k] = true
		m.memo[k] = eq
	}
	return eq
}

// lcs returns the source/target index pairs that form a longest
// common subsequence. The tie-breaking of the reference dynamic
// program is preserved exactly.
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

// unorderedEqual reports whether the two arrays contain the same
// values regardless of order, honoring duplicates. It uses a
// multiset match: hashes prefilter candidates, and each equal
// target element consumes one distinct source occurrence, so hash
// collisions and repeated values cannot be mistaken for a match.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	used := make([]bool, len(m.src))
	for j := 0; j < len(m.tgt); j++ {
		matched := -1
		for i := 0; i < len(m.src); i++ {
			if !used[i] && m.equal(i, j) {
				matched = i
				break
			}
		}
		if matched == -1 {
			return false
		}
		used[matched] = true
	}
	return true
}

// equalSrc reports whether v is deeply equal to src[i].
// It routes array-internal probes through the memo table and
// counts every fallback comparison for diagnostics.
func (m *arrayMatcher) equalSrc(i int, v interface{}) bool {
	if tj, ok := m.tgtByValue(v); ok {
		return m.equal(i, tj)
	}
	m.d.stats.EqualDeep++
	return deepEqual(m.src[i], v)
}

// tgtByValue reports the target index holding v, when v was
// derived from this matcher's target slice. It lets move/copy
// candidate probes reuse memoized array comparisons without
// keeping a parallel equality implementation.
func (m *arrayMatcher) tgtByValue(v interface{}) (int, bool) {
	for j := range m.tgt {
		if m.tgt[j] == v {
			return j, true
		}
	}
	return 0, false
}
