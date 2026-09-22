package jsondiff

// A sliceMatcher unifies the responsibilities of equality
// testing, hashing and comparison accounting during the
// comparison of two arrays. The digest of each element is
// computed exactly once, when the matcher is created, and
// the LCS computation queries the equality of elements by
// their indices through the matcher.
type sliceMatcher struct {
	src, tgt []interface{}
	sh, th   []uint64
	d        *Differ
}

// newSliceMatcher returns a matcher for the given source
// and target slices, computing the digest of each element
// of both slices once.
func newSliceMatcher(d *Differ, src, tgt []interface{}) *sliceMatcher {
	m := &sliceMatcher{
		src: src,
		tgt: tgt,
		sh:  make([]uint64, len(src)),
		th:  make([]uint64, len(tgt)),
		d:   d,
	}
	for i := range src {
		m.sh[i] = d.digest(src[i], false)
	}
	for j := range tgt {
		m.th[j] = d.digest(tgt[j], false)
	}
	return m
}

// equal reports whether the elements src[i] and tgt[j]
// are equal. The digests of the elements are compared
// first, and a deep equality comparison is performed only
// to confirm a digest match, so that elements with equal
// digests but different values (hash collisions) cannot
// be mis-paired. Each deep comparison is accounted for in
// the diagnostic statistics of the differ.
func (m *sliceMatcher) equal(i, j int) bool {
	if m.sh[i] != m.th[j] {
		return false
	}
	m.d.stats.EqualityComparisons++
	return deepEqual(m.src[i], m.tgt[j])
}
