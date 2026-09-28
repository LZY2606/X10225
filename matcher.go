package jsondiff

// Stats holds diagnostic counters describing the work performed
// by a comparison. It is strictly diagnostic: it never influences
// the generated patch, and counters are reset at the beginning of
// every [Differ.Compare] call, so they are isolated between
// concurrent comparisons that use their own Differ instances.
//
// Counters may change between releases; they must not be relied
// upon to make decisions about the content of a patch.
type Stats struct {
	// HashDigests is the number of value hash digests that
	// were computed during matching. Each distinct value is
	// digested at most once per matching phase.
	HashDigests int

	// EqualityChecks is the number of actual deep-equality
	// comparisons performed between two values. Hash digest
	// comparisons are not counted.
	EqualityChecks int

	// HashCollisions is the number of times two values shared
	// the same hash digest but were found different by a
	// deep-equality comparison. A non-zero value proves that
	// matching is not driven by hashes alone.
	HashCollisions int

	// LCSMatches is the total number of index pairs reported
	// by longest-common-subsequence array matchings.
	LCSMatches int

	// MoveCandidates is the number of move candidates found
	// while factorizing operations.
	MoveCandidates int

	// CopyCandidates is the number of copy candidates found
	// while factorizing operations.
	CopyCandidates int
}

// digestOf computes the hash digest of v and records it in the
// diagnostic counters. All hash digest computations inside the
// Differ must go through this function so that the candidate
// values are accounted for exactly once.
func (d *Differ) digestOf(v interface{}, sort bool) uint64 {
	d.stats.HashDigests++
	return d.hasher.digest(v, sort)
}

// equalValues reports whether a and b are deep-equal. The
// precomputed digests da and db act as a fast negative filter:
// two values with different digests are not equal, with the
// sole exception of the float64 values -0.0 and +0.0, which
// compare equal but have a different binary representation.
//
// Whenever the digests are equal, the result is confirmed with
// a deep comparison, so hash collisions can never cause two
// distinct values to be paired.
func (d *Differ) equalValues(a, b interface{}, da, db uint64) bool {
	if da != db {
		fa, ok1 := a.(float64)
		fb, ok2 := b.(float64)
		if ok1 && ok2 {
			d.stats.EqualityChecks++
			return fa == fb
		}
		return false
	}
	d.stats.EqualityChecks++
	if !deepEqual(a, b) {
		d.stats.HashCollisions++
		return false
	}
	return true
}

// arrayMatcher unifies the four concerns involved in array
// comparison:
//
//   - equality, through [Differ.equalValues],
//   - hashing, with one digest per element computed once,
//   - longest common subsequence, through [arrayMatcher.lcsPairs],
//   - move/copy candidate discovery, which reuses the digests
//     and equality logic held by the owning Differ.
//
// The digest of every source and target element is computed a
// single time, and the result of every equality comparison is
// memoized so that the LCS backtracking does not recompute
// candidates that were already evaluated while filling the
// dynamic-programming table.
type arrayMatcher struct {
	d    *Differ
	src  []interface{}
	tgt  []interface{}
	srcH []uint64
	tgtH []uint64
	// memo holds lazily-computed equality results in row-major
	// order (source index first): 0 unknown, 1 equal, -1 unequal.
	memo []int8
}

// newArrayMatcher prepares the matching of src and tgt. All
// element digests are computed here, once, before any
// equality comparison or LCS computation.
func (d *Differ) newArrayMatcher(src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		d:   d,
		src: src,
		tgt: tgt,
	}
	m.srcH = make([]uint64, len(src))
	for i, v := range src {
		m.srcH[i] = d.digestOf(v, false)
	}
	m.tgtH = make([]uint64, len(tgt))
	for j, v := range tgt {
		m.tgtH[j] = d.digestOf(v, false)
	}
	if n := len(src) * len(tgt); n > 0 {
		m.memo = make([]int8, n)
	}
	return m
}

// equal reports whether src[i] and tgt[j] are deep-equal. The
// outcome is memoized, so each candidate pair is evaluated at
// most once for the whole matching.
func (m *arrayMatcher) equal(i, j int) bool {
	k := i*len(m.tgt) + j
	if v := m.memo[k]; v != 0 {
		return v > 0
	}
	eq := m.d.equalValues(m.src[i], m.tgt[j], m.srcH[i], m.tgtH[j])
	if eq {
		m.memo[k] = 1
	} else {
		m.memo[k] = -1
	}
	return eq
}

// lcsPairs computes the longest common subsequence of the
// source and target slices and returns the matching index
// pairs, indices into the source and target slices
// respectively.
func (m *arrayMatcher) lcsPairs() [][2]int {
	t := make([][]int, len(m.src)+1)

	for i := 0; i <= len(m.src); i++ {
		t[i] = make([]int, len(m.tgt)+1)
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
	i, j := len(m.src), len(m.tgt)
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
