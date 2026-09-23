package jsondiff

import "reflect"

// Stats holds diagnostic counters that describe the work
// performed by a Differ while matching the elements of
// arrays during the last comparison.
//
// The counters are purely informational: they have no
// effect on the generated patch, are reset at the beginning
// of each comparison, and are kept per Differ instance so
// that concurrent comparisons never share them.
type Stats struct {
	// HashComputations is the number of hash digests
	// computed while matching array elements and while
	// discovering move/copy candidates.
	HashComputations int
	// EqualityChecks is the number of deep-equality checks
	// performed to confirm or reject candidate pairs,
	// including the checks required to disambiguate hash
	// collisions.
	EqualityChecks int
	// MemoizedChecks is the number of equality lookups
	// served from the memoized candidate set, that is,
	// pairs that did not require a new deep-equality check.
	MemoizedChecks int
}

// equal reports whether two values are deeply equal, and
// accounts the comparison in the diagnostic stats.
func (d *Differ) equal(src, tgt interface{}) bool {
	d.stats.EqualityChecks++
	return deepEqual(src, tgt)
}

// digest returns the hash of the given value, and accounts
// the computation in the diagnostic stats.
func (d *Differ) digest(v interface{}, sort bool) uint64 {
	d.stats.HashComputations++
	return d.hasher.digest(v, sort)
}

// equivalentEqual reports whether src and tgt are equal
// under the semantics of the Equivalent option: the order
// of array elements is not significant, recursively.
// It is used to confirm candidate pairs that share the
// same hash digest, so that distinct values colliding on
// a digest are never mispaired.
func (d *Differ) equivalentEqual(src, tgt interface{}) bool {
	d.stats.EqualityChecks++

	st, tt := jsonTypeSwitch(src), jsonTypeSwitch(tgt)
	if st != tt {
		return false
	}
	switch st {
	case jsonArray:
		return d.unorderedDeepEqualSlice(
			src.([]interface{}),
			tgt.([]interface{}),
		)
	case jsonObject:
		oobj := src.(map[string]interface{})
		nobj := tgt.(map[string]interface{})

		if len(oobj) != len(nobj) {
			return false
		}
		for k, v1 := range oobj {
			v2, ok := nobj[k]
			if !ok || !d.equivalentEqual(v1, v2) {
				return false
			}
		}
		return true
	default:
		if st == jsonInvalid {
			// Values outside the JSON type system cannot
			// be compared with deepEqual; fall back to a
			// shallow reflection-based comparison.
			return reflect.DeepEqual(src, tgt)
		}
		return deepEqual(src, tgt)
	}
}

// An arrayMatcher unifies the responsibilities of equality
// testing, hashing and longest common subsequence
// computation for a pair of arrays, and serves the
// move/copy candidate discovery of the Differ.
//
// The equality relation between the elements of the source
// and target arrays is computed at most once per index
// pair: a hash pre-filter rejects the pairs that cannot be
// equal (equal values always have equal digests), and the
// remaining candidates are confirmed with a single
// deep-equality check, memoized for the subsequent lookups
// performed by the LCS backtracking. The confirmation
// guarantees that distinct values colliding on a hash
// digest are never mispaired.
type arrayMatcher struct {
	differ *Differ
	src    []interface{}
	tgt    []interface{}
	shash  []uint64
	thash  []uint64
	memo   map[[2]int]bool
}

// newArrayMatcher returns a matcher for the given source
// and target arrays. If the Differ d is not nil, the
// element digests are computed once, up front, and all the
// comparisons are accounted in its diagnostic stats.
func newArrayMatcher(d *Differ, src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		differ: d,
		src:    src,
		tgt:    tgt,
	}
	if d != nil {
		m.shash = make([]uint64, len(src))
		for i := range src {
			m.shash[i] = d.digest(src[i], false)
		}
		m.thash = make([]uint64, len(tgt))
		for j := range tgt {
			m.thash[j] = d.digest(tgt[j], false)
		}
	}
	return m
}

// equal reports whether src[i] and tgt[j] are equal. The
// result of each index pair is computed at most once.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.shash != nil && m.shash[i] != m.thash[j] {
		// The hash pre-filter rejects the pair without a
		// deep-equality check; equal values always have
		// equal digests.
		return false
	}
	k := [2]int{i, j}
	if eq, ok := m.memo[k]; ok {
		if m.differ != nil {
			m.differ.stats.MemoizedChecks++
		}
		return eq
	}
	var eq bool
	if m.differ != nil {
		eq = m.differ.equal(m.src[i], m.tgt[j])
	} else {
		eq = deepEqual(m.src[i], m.tgt[j])
	}
	if m.memo == nil {
		m.memo = make(map[[2]int]bool)
	}
	m.memo[k] = eq
	return eq
}

// pairs computes the longest common subsequence of the
// source and target arrays and returns the index pairs of
// the LCS, that is, the indices into the source and target
// arrays where the LCS items are located.
func (m *arrayMatcher) pairs() [][2]int {
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
