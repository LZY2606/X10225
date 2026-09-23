package jsondiff

// diffStats gathers diagnostic counters about the work performed
// by the unified array matching and move/copy candidate discovery
// during a single [Differ.Compare] call.
//
// The counters are purely diagnostic: they never influence the
// generated patch. They are reset at the beginning of every
// comparison, and each [Differ] instance owns its own counters,
// so repeated and concurrent comparisons are isolated from each
// other. The counters live behind an unexported field because
// they are consumed by the project's tests, while production
// code uses the exact same matching path as the tests.
type diffStats struct {
	// hashes counts the digest computations performed while
	// matching array elements and discovering move/copy
	// candidates. The digest of each element involved in a
	// matching is computed at most once.
	hashes int

	// equalities counts the deep-equality evaluations between
	// pairs of values while matching array elements (LCS and
	// unordered/equivalent matching). Each pair of elements is
	// evaluated at most once.
	equalities int

	// candidates counts the deep-equality verifications performed
	// between a newly added/removed value and a registered
	// move/copy candidate. Digests are used as a prefilter, and
	// a candidate is confirmed only if it is deeply equal.
	candidates int
}

// arrayMatcher unifies the responsibilities of equality testing,
// hashing and longest-common-subsequence matching for a single
// pair of arrays.
//
// The digest of every element is computed exactly once. Each
// pair of elements is evaluated for equality at most once: the
// digests act as an O(1) prefilter, and a positive prefilter is
// always confirmed by a deep-equality evaluation, so a hash
// collision can never pair different values.
type arrayMatcher struct {
	src   []interface{}
	tgt   []interface{}
	dsrc  []uint64
	dtgt  []uint64
	eq    map[[2]int]bool
	stats *diffStats
}

// newArrayMatcher creates a matcher for the given pair of arrays.
// When sort is true, nested arrays are hashed in sorted order, as
// required by the recursive unordered/equivalent matching.
func newArrayMatcher(src, tgt []interface{}, sort bool, stats *diffStats) *arrayMatcher {
	m := &arrayMatcher{
		src:   src,
		tgt:   tgt,
		eq:    make(map[[2]int]bool),
		stats: stats,
	}
	h := hasher{}
	m.dsrc = m.digestAll(&h, src, sort)
	m.dtgt = m.digestAll(&h, tgt, sort)
	return m
}

func (m *arrayMatcher) digestAll(h *hasher, vals []interface{}, sort bool) []uint64 {
	digests := make([]uint64, len(vals))
	for i, v := range vals {
		digests[i] = h.digest(v, sort)
		m.stats.hashes++
	}
	return digests
}

// equal reports whether the source element at i and the target
// element at j are deeply equal. The result of each pair is
// computed once and memoized for subsequent evaluations.
func (m *arrayMatcher) equal(i, j int) bool {
	k := [2]int{i, j}
	if eq, ok := m.eq[k]; ok {
		return eq
	}
	var eq bool
	// Digests are deterministic for a given value: different
	// digests prove that the values are not equal, while equal
	// digests only make equality possible and must be verified.
	if m.dsrc[i] == m.dtgt[j] {
		m.stats.equalities++
		eq = deepEqual(m.src[i], m.tgt[j])
	}
	m.eq[k] = eq
	return eq
}

// lcs computes the longest common subsequence of the two arrays
// and returns the index pairs of the LCS, that is, the indices
// into the source and target arrays where the matched items are
// located. The dynamic-programming fill and the backtrack share
// the same memoized equality results, so each pair is compared
// at most once.
func (m *arrayMatcher) lcs() [][2]int {
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

// equivalentEqual reports whether src and tgt are equal when the
// elements of arrays are considered unordered, recursively.
//
// Target array elements are bucketed by digest, and every source
// element is matched with a distinct candidate of its bucket.
// Bucketing makes the matching near-linear, while the recursive
// verification guarantees that hash collisions can never pair
// different values and that duplicate values are matched with the
// right multiplicity.
func (d *Differ) equivalentEqual(src, tgt interface{}) bool {
	d.stats.equalities++

	st := jsonTypeSwitch(src)
	if st != jsonTypeSwitch(tgt) {
		return false
	}
	switch st {
	case jsonArray:
		oarr := src.([]interface{})
		narr := tgt.([]interface{})

		if len(oarr) != len(narr) {
			return false
		}
		buckets := make(map[uint64][]int, len(narr))
		for j, v := range narr {
			k := d.hasher.digest(v, true)
			d.stats.hashes++
			buckets[k] = append(buckets[k], j)
		}
		matched := make([]bool, len(narr))
		for _, v := range oarr {
			k := d.hasher.digest(v, true)
			d.stats.hashes++

			found := false
			for _, j := range buckets[k] {
				if !matched[j] && d.equivalentEqual(v, narr[j]) {
					matched[j] = true
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
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
			// The values are not recognized JSON types (this only
			// happens when the method is called directly with Go
			// values such as ints that json.Unmarshal never yields).
			// They cannot be faithfully compared, so the decision
			// follows the digest, preserving the legacy behavior
			// instead of guessing a deep equality.
			d.stats.hashes += 2
			return d.hasher.digest(src, true) == d.hasher.digest(tgt, true)
		}
		// Recognized scalar values are always compared directly:
		// unlike a digest match, a type mismatch (e.g. false and
		// null) can never pair two different values.
		return deepEqual(src, tgt)
	}
}
