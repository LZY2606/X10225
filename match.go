package jsondiff

// diffStats holds the diagnostic counters accumulated by a
// Differ during a comparison. The counters are reset at the
// beginning of each call to Differ.Compare, and by Differ.Reset.
type diffStats struct {
	equalityComparisons int
	hashComputations    int
}

// Stats is a snapshot of the diagnostic counters of a Differ.
// It quantifies the comparison work performed for array element
// matching and move/copy candidate discovery during the last
// call to Differ.Compare.
//
// The counters are strictly diagnostic: they have no effect on
// the generated patch. Each Differ instance owns its counters,
// which are reset at the beginning of each comparison, so
// concurrent comparisons performed by distinct Differ instances
// (such as the ones created by the Compare, CompareJSON and
// CompareWithoutMarshal functions) are fully isolated.
type Stats struct {
	// EqualityComparisons is the number of deep-equality
	// comparisons performed to confirm the equality of array
	// elements and move candidates whose hash digests collide.
	EqualityComparisons int

	// HashComputations is the number of hash digests computed
	// for JSON values while matching array elements and while
	// discovering move/copy candidates.
	HashComputations int
}

// Stats returns a snapshot of the diagnostic counters of the
// Differ for its most recent comparison.
func (d *Differ) Stats() Stats {
	return Stats{
		EqualityComparisons: d.stats.equalityComparisons,
		HashComputations:    d.stats.hashComputations,
	}
}

// equal reports whether two JSON values are deeply equal, and
// accounts the comparison in the differ statistics.
func (d *Differ) equal(src, tgt interface{}) bool {
	d.stats.equalityComparisons++
	return deepEqual(src, tgt)
}

// digest returns the hash of the given JSON value, and accounts
// the computation in the differ statistics.
func (d *Differ) digest(val interface{}, sort bool) uint64 {
	d.stats.hashComputations++
	return d.hasher.digest(val, sort)
}

// A matcher unifies the responsibilities of element equality,
// hashing and candidate discovery for the comparison of two
// arrays. It is the single implementation used by the production
// diff path (see Differ.compareArraysLCS); it is not exported,
// and is exercised by the tests through the public entrypoints.
//
// The hash digest of each element is computed exactly once, and
// serves as a negative filter: a deep-equality comparison is
// only performed to confirm the equality of the pairs of
// elements whose digests collide, and its result is memoized so
// that each pair of indices is compared at most once. This
// guarantees that duplicated elements are never mismatched by
// hash collisions.
type matcher struct {
	src     []interface{}
	tgt     []interface{}
	srcHash []uint64
	tgtHash []uint64
	eq      []uint8
	d       *Differ
}

// Tri-state of a memoized pair comparison.
const (
	matchUnknown uint8 = iota
	matchFalse
	matchTrue
)

// newMatcher returns a matcher for the given source and target
// slices. The hash digests of all the elements are computed
// upfront, once per element.
func newMatcher(d *Differ, src, tgt []interface{}) *matcher {
	m := &matcher{
		src:     src,
		tgt:     tgt,
		srcHash: make([]uint64, len(src)),
		tgtHash: make([]uint64, len(tgt)),
		d:       d,
	}
	for i, v := range src {
		m.srcHash[i] = d.digest(v, false)
	}
	for i, v := range tgt {
		m.tgtHash[i] = d.digest(v, false)
	}
	return m
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// The result is computed at most once for each pair of indices.
func (m *matcher) equal(i, j int) bool {
	if m.srcHash[i] != m.tgtHash[j] {
		// The digests differ, the elements cannot be equal.
		return false
	}
	k := i*len(m.tgt) + j
	if m.eq != nil {
		if v := m.eq[k]; v != matchUnknown {
			return v == matchTrue
		}
	} else {
		// Lazily allocate the memoization matrix on the
		// first digest collision.
		m.eq = make([]uint8, len(m.src)*len(m.tgt))
	}
	r := m.d.equal(m.src[i], m.tgt[j])
	if r {
		m.eq[k] = matchTrue
	} else {
		m.eq[k] = matchFalse
	}
	return r
}
