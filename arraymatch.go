package jsondiff

// Stats reports diagnostic counters gathered by a Differ
// during the last comparison. The counters quantify the work
// performed by array matching (hashing, deep-equality and the
// longest common subsequence) and by move/copy candidate
// discovery.
//
// Stats are purely diagnostic: they never influence the
// generated patch, and they are kept per Differ instance so
// that concurrent comparisons performed with distinct Differ
// values (or the top-level Compare/CompareJSON functions, which
// always use a fresh Differ) are isolated from each other.
type Stats struct {
	// HashComputations is the number of digest computations
	// performed for array matching and copy candidate discovery.
	HashComputations int
	// EqualityChecks is the number of deep-equality confirmations
	// performed between pairs of array elements. Each pair of
	// indices is confirmed at most once per array comparison.
	EqualityChecks int
	// MoveCopyCandidates is the number of candidate pairs
	// examined while discovering move and copy operations.
	MoveCopyCandidates int
}

// Stats returns a copy of the diagnostic counters of the
// Differ for its last comparison. The counters are reset at
// the beginning of each call to Compare and by Reset.
func (d *Differ) Stats() Stats {
	return d.stats
}

// digest returns the hash of the given value. It is the single
// entry point used by array matching, factorization and copy
// candidate discovery, so that all of them share the same
// digest source.
//
// The digestFunc field of the Differ is nil in production and
// exists only so that in-package tests can exercise the
// production matching logic with adversarial (e.g. colliding)
// digests. It is unexported and cannot be set by callers.
func (d *Differ) digest(v interface{}, sort bool) uint64 {
	if d.digestFunc != nil {
		return d.digestFunc(v, sort)
	}
	return d.hasher.digest(v, sort)
}

// arrayMatcher unifies the responsibilities of hashing,
// deep-equality and longest-common-subsequence computation for
// a single pair of arrays. Each element is hashed exactly once,
// and the equality of each pair of indices is confirmed with
// deepEqual at most once; the LCS table construction and its
// backtracking share the same memoized results instead of
// recomputing them.
type arrayMatcher struct {
	src, tgt []interface{}
	srcHash  []uint64
	tgtHash  []uint64
	// eq and ne are bitsets indexed by i*words+j/64 that
	// memoize confirmed-equal and confirmed-unequal pairs.
	eq, ne []uint64
	words  int
	digest func(interface{}, bool) uint64
	stats  *Stats
}

func newArrayMatcher(src, tgt []interface{}, digest func(interface{}, bool) uint64, stats *Stats) *arrayMatcher {
	m := &arrayMatcher{
		src:     src,
		tgt:     tgt,
		srcHash: make([]uint64, len(src)),
		tgtHash: make([]uint64, len(tgt)),
		words:   (len(tgt) + 63) / 64,
		digest:  digest,
		stats:   stats,
	}
	if m.words > 0 {
		m.eq = make([]uint64, len(src)*m.words)
		m.ne = make([]uint64, len(src)*m.words)
	}
	for i := range src {
		m.srcHash[i] = m.hash(src[i])
	}
	for j := range tgt {
		m.tgtHash[j] = m.hash(tgt[j])
	}
	return m
}

func (m *arrayMatcher) hash(v interface{}) uint64 {
	if m.stats != nil {
		m.stats.HashComputations++
	}
	return m.digest(v, false)
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// Pairs with different digests are never equal; pairs with
// equal digests are confirmed with deepEqual at most once, so
// that hash collisions can never mis-pair distinct values.
func (m *arrayMatcher) equal(i, j int) bool {
	w := i*m.words + j/64
	bit := uint64(1) << (uint(j) % 64)

	if m.eq[w]&bit != 0 {
		return true
	}
	if m.ne[w]&bit != 0 {
		return false
	}
	if m.srcHash[i] != m.tgtHash[j] {
		m.ne[w] |= bit
		return false
	}
	if m.stats != nil {
		m.stats.EqualityChecks++
	}
	if deepEqual(m.src[i], m.tgt[j]) {
		m.eq[w] |= bit
		return true
	}
	m.ne[w] |= bit
	return false
}

// lcs computes the longest common subsequence of the matched
// slices and returns the index pairs of the LCS, that is, the
// indices into the source and target slices where the LCS
// items are located. It produces the same pairs as the
// historical two-pass implementation, but each pair of indices
// is evaluated only once.
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
