package jsondiff

// An arrayMatcher unifies the equality and hashing concerns
// of the array comparison algorithms. The digest of each
// element of the source and target slices is computed exactly
// once, and a deep-equality comparison is only ever performed
// for a pair of elements whose digests collide; the result of
// each of those comparisons is memoized so that every candidate
// pair is evaluated a single time, no matter how many consumers
// (LCS, unordered equality, move/copy discovery) query it.
//
// When the sort flag is set, the matcher implements the
// unordered equality relation of the Equivalent option: the
// digests are computed with sorted arrays, and candidate pairs
// are verified with unorderedDeepEqualValues.
type arrayMatcher struct {
	d      *Differ
	src    []interface{}
	tgt    []interface{}
	sort   bool
	shash  []uint64
	thash  []uint64
	tindex map[uint64][]int // target indices grouped by digest
	memo   map[[2]int]bool  // memoized deep-equality results
}

func newArrayMatcher(d *Differ, src, tgt []interface{}, sort bool) *arrayMatcher {
	m := &arrayMatcher{
		d:      d,
		src:    src,
		tgt:    tgt,
		sort:   sort,
		shash:  make([]uint64, len(src)),
		thash:  make([]uint64, len(tgt)),
		tindex: make(map[uint64][]int, len(tgt)),
	}
	for i := range src {
		m.shash[i] = d.digest(src[i], sort)
	}
	for j := range tgt {
		h := d.digest(tgt[j], sort)
		m.thash[j] = h
		m.tindex[h] = append(m.tindex[h], j)
	}
	return m
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// Pairs with different digests are never compared, and the
// comparison of a candidate pair happens at most once.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.shash[i] != m.thash[j] {
		return false
	}
	k := [2]int{i, j}
	eq, ok := m.memo[k]
	if !ok {
		if m.sort {
			eq = unorderedDeepEqualValues(m.d, m.src[i], m.tgt[j])
		} else {
			eq = m.d.valuesEqual(m.src[i], m.tgt[j])
		}
		if m.memo == nil {
			m.memo = make(map[[2]int]bool)
		}
		m.memo[k] = eq
	}
	return eq
}

// unorderedEqual reports whether the source and target slices
// contain the same elements, regardless of their order. Each
// source element is paired with a distinct, deeply equal target
// element, which prevents duplicate values from being mismatched
// by digest collisions.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	matched := make([]bool, len(m.tgt))
outer:
	for i := range m.src {
		for _, j := range m.tindex[m.shash[i]] {
			if !matched[j] && m.equal(i, j) {
				matched[j] = true
				continue outer
			}
		}
		return false
	}
	return true
}

// unorderedDeepEqualValues reports whether two JSON values are
// deeply equal, considering arrays as unordered collections of
// elements, recursively.
func unorderedDeepEqualValues(d *Differ, src, tgt interface{}) bool {
	st := jsonTypeSwitch(src)
	if st == jsonInvalid {
		panic(invalidJSONTypeError{t: src})
	}
	tt := jsonTypeSwitch(tgt)
	if tt == jsonInvalid {
		panic(invalidJSONTypeError{t: tgt})
	}
	if st != tt {
		return false
	}
	switch st {
	case jsonArray:
		return newArrayMatcher(d,
			src.([]interface{}),
			tgt.([]interface{}),
			true,
		).unorderedEqual()
	case jsonObject:
		oobj := src.(map[string]interface{})
		nobj := tgt.(map[string]interface{})

		if len(oobj) != len(nobj) {
			return false
		}
		for k, v1 := range oobj {
			v2, ok := nobj[k]
			if !ok || !unorderedDeepEqualValues(d, v1, v2) {
				return false
			}
		}
		return true
	default:
		return d.valuesEqual(src, tgt)
	}
}
