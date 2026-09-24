package jsondiff

// arrayMatcher consolidates the four responsibilities involved
// in comparing two JSON arrays:
//
//  1. Equality: every old/new element pair is compared at most
//     once through [arrayMatcher.equal].
//  2. Hashing: a digest is computed once per element and is used
//     solely as a pre-filter; equal digests are always confirmed
//     by [deepEqual], so digest collisions can never pair
//     distinct values together.
//  3. LCS: the longest common subsequence is built on top of the
//     memoized equality matrix, without re-comparing values.
//  4. Move discovery: removed old elements are tagged and looked
//     up by an added new element through the same memoized
//     comparisons, ensuring that a candidate is computed once.
//
// A new matcher is created for every array that is compared; it
// therefore carries no state between concurrent comparisons.
type arrayMatcher struct {
	src []interface{}
	tgt []interface{}

	// srcHash[i]/tgtHash[j] hold the digest of src[i]/tgt[j].
	// A value of zero means the digest has not been computed yet.
	srcHash []uint64
	tgtHash []uint64

	// eq[i][j] is the memoized result of deepEqual(src[i],tgt[j]):
	//   0 => not compared yet
	//   1 => equal
	//   2 => unequal
	eq [][]uint8

	// Diagnostics, isolated to this comparison.
	comparisons int // old/new element pairs confirmed with deepEqual
	hashSkips   int // pairs ruled out solely by differing digests
	digests     int // digest computations performed

	// removeTag maps the old-element index of a remove operation
	// emitted by this matcher to the unique tag carried by the
	// operation, which allows move discovery to reuse the
	// equality matrix instead of comparing values again.
	removeTag map[int]int

	// Test-only failure injection. When faultAfter is greater
	// than or equal to zero, the matcher panics with errMatcherFault
	// after performing faultAfter deepEqual comparisons.
	faultAfter int
}

func newArrayMatcher(src, tgt []interface{}) *arrayMatcher {
	return &arrayMatcher{
		src:        src,
		tgt:        tgt,
		srcHash:    make([]uint64, len(src)),
		tgtHash:    make([]uint64, len(tgt)),
		eq:         make([][]uint8, len(src)),
		removeTag:  make(map[int]int),
		faultAfter: -1,
	}
}

// digestSrc returns the digest of src[i], computing it once.
func (m *arrayMatcher) digestSrc(d *Differ, i int) uint64 {
	if h := m.srcHash[i]; h != 0 {
		return h
	}
	h := d.elementDigest(m.src[i])
	m.srcHash[i] = h
	m.digests++
	return h
}

// digestTgt returns the digest of tgt[j], computing it once.
func (m *arrayMatcher) digestTgt(d *Differ, j int) uint64 {
	if h := m.tgtHash[j]; h != 0 {
		return h
	}
	h := d.elementDigest(m.tgt[j])
	m.tgtHash[j] = h
	m.digests++
	return h
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// The result of each pair is computed exactly once. The digest
// is a fast negative pre-filter only: when digests differ the
// values are known to be unequal without a deepEqual call, and
// when they match deepEqual is used as the authoritative check
// to remain safe against digest collisions.
func (m *arrayMatcher) equal(d *Differ, i, j int) bool {
	row := m.eq[i]
	if row == nil {
		row = make([]uint8, len(m.tgt))
		m.eq[i] = row
	}
	switch row[j] {
	case 1:
		return true
	case 2:
		return false
	}

	var equal bool
	if m.digestSrc(d, i) != m.digestTgt(d, j) {
		m.hashSkips++
		equal = false
	} else {
		equal = d.countEqual(m.src[i], m.tgt[j])
		m.comparisons++

		if m.faultAfter >= 0 && m.comparisons > m.faultAfter {
			panic(errMatcherFault)
		}
	}
	if equal {
		row[j] = 1
	} else {
		row[j] = 2
	}
	return equal
}

// pairs returns the index pairs of the longest common
// subsequence of src and tgt. It uses the exact same dynamic
// programming table and backtracking tie-breaking as the legacy
// [lcs] implementation, only routing equality checks through the
// memoized matcher so that pairs are never compared twice.
func (m *arrayMatcher) pairs(d *Differ) [][2]int {
	t := make([][]int, len(m.src)+1)
	for i := 0; i <= len(m.src); i++ {
		t[i] = make([]int, len(m.tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if m.equal(d, i-1, j-1) {
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
		case m.equal(d, i-1, j-1):
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

// tagRemove records that a remove operation carrying tag has been
// emitted for old element src[i].
func (m *arrayMatcher) tagRemove(i, tag int) {
	m.removeTag[i] = tag
}

// findRemoved scans the current patch for the first remove
// operation whose old value is deeply equal to tgt[j], mirroring
// the historical first-match consumption order. Remove
// operations emitted by this matcher answer through the memoized
// equality matrix for free; other removes (including nested
// arrays and objects) fall back to a regular deepEqual check,
// which preserves the global factorization semantics.
func (m *arrayMatcher) findRemoved(d *Differ, patch Patch, j int) int {
	for i := 0; i < len(patch); i++ {
		op := patch[i]
		if op.Type != OperationRemove {
			continue
		}
		eq := false
		if si, ok := m.taggedIndex(op.matchTag); ok {
			eq = m.equal(d, si, j)
		} else {
			eq = d.countEqual(op.OldValue, m.tgt[j])
		}
		if eq {
			return i
		}
	}
	return -1
}

// taggedIndex reports whether tag belongs to this matcher and,
// if so, returns the associated old-element index.
func (m *arrayMatcher) taggedIndex(tag int) (int, bool) {
	if tag == 0 {
		return 0, false
	}
	for si, t := range m.removeTag {
		if t == tag {
			return si, true
		}
	}
	return 0, false
}

// lcs keeps the historical package-level entry point as a thin
// adapter over [arrayMatcher], so there is no parallel algorithm
// to maintain.
func lcs(src, tgt []interface{}) [][2]int {
	m := newArrayMatcher(src, tgt)
	d := &Differ{}
	return m.pairs(d)
}
