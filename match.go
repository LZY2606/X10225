package jsondiff

// Stats holds diagnostic counters that quantify the work
// performed by a Differ while matching the elements of arrays
// and discovering move/copy candidates.
//
// The counters are reset at the beginning of each call to
// Compare and are intended for observability only; they never
// influence the content of the generated patch. Each Differ
// instance collects its own counters, and the package-level
// comparison functions use a dedicated Differ per call, so
// concurrent comparisons are isolated from each other.
type Stats struct {
	// HashDigests is the number of hash digests computed for
	// array elements. The digest of each element of a pair of
	// compared arrays is computed exactly once.
	HashDigests int

	// EqualChecks is the number of deep-equality checks
	// performed between pairs of array elements while
	// computing their longest common subsequence. The equality
	// of a pair of elements is evaluated at most once.
	EqualChecks int

	// MoveCopyChecks is the number of deep-equality checks
	// performed while discovering move and copy candidates
	// (see the Factorize option).
	MoveCopyChecks int
}

// A matcher unifies the responsibilities of hashing, equality
// and longest-common-subsequence computation for the elements
// of a pair of arrays.
//
// The digest of each element is computed exactly once, and the
// equality of a pair of elements is evaluated at most once:
// digests act as a pre-filter for the candidate pairs, and the
// candidates are systematically verified with a deep-equality
// check, so that duplicate elements are never mismatched by
// colliding hashes.
type matcher struct {
	src   []interface{}
	tgt   []interface{}
	shash []uint64
	thash []uint64
	memo  map[[2]int]bool
	stats *Stats
}

// newMatcher returns a matcher for the elements of src and
// tgt, and computes the digest of each element exactly once.
// Values that are not valid JSON types are rejected with a
// panic, which the package-level comparison functions recover
// and return as an error.
func newMatcher(src, tgt []interface{}, st *Stats) *matcher {
	h := hasher{strict: true}
	m := &matcher{
		src:   src,
		tgt:   tgt,
		shash: make([]uint64, len(src)),
		thash: make([]uint64, len(tgt)),
		stats: st,
	}
	for i, v := range src {
		m.shash[i] = h.digest(v, false)
	}
	for j, v := range tgt {
		m.thash[j] = h.digest(v, false)
	}
	st.HashDigests += len(src) + len(tgt)
	return m
}

// equal reports whether the elements src[i] and tgt[j] are
// deeply equal. The result of the comparison is computed at
// most once for each pair of indices; subsequent calls return
// the memoized result.
func (m *matcher) equal(i, j int) bool {
	if m.shash[i] != m.thash[j] {
		// Distinct digests imply that the elements cannot be
		// deeply equal; no further comparison is necessary.
		return false
	}
	k := [2]int{i, j}
	if eq, ok := m.memo[k]; ok {
		return eq
	}
	// Equal digests are only a hint: verify the candidate
	// pair with a deep-equality check, so that colliding
	// hashes and duplicate values are never mismatched.
	eq := deepEqual(m.src[i], m.tgt[j])
	m.stats.EqualChecks++
	if m.memo == nil {
		m.memo = make(map[[2]int]bool)
	}
	m.memo[k] = eq
	return eq
}

// lcsPairs computes the longest common subsequence of the
// matched slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices where the
// LCS items are located.
func (m *matcher) lcsPairs() [][2]int {
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
