package jsondiff

// compareStats accumulates diagnostic counters for a single
// comparison. The counters are purely diagnostic: they never
// influence the generated patch, and every comparison run
// starts with a zeroed value, so concurrent comparisons that
// use distinct Differ instances are fully isolated.
type compareStats struct {
	// arrayEqualChecks counts old/new element equality checks
	// performed while matching arrays (LCS fill and backtrack,
	// each unordered pair evaluated at most once).
	arrayEqualChecks int
	// multisetEqualChecks counts equality checks performed by
	// the unordered (multiset) array equivalence test.
	multisetEqualChecks int
	// moveProbes counts equality checks against live remove
	// candidates when searching for a move source.
	moveProbes int
	// copyProbes counts equality checks against unchanged
	// locations when searching for a copy source.
	copyProbes int
	// hashCollisions counts how many digest buckets contained
	// entries whose digest matched but whose values did not.
	hashCollisions int
}

// Stats is the diagnostic snapshot of the counters collected
// during the most recent comparison performed by the Differ.
type Stats struct {
	ArrayEqualChecks    int
	MultisetEqualChecks int
	MoveProbes          int
	CopyProbes          int
	HashCollisions      int
}

func (s compareStats) snapshot() Stats {
	return Stats{
		ArrayEqualChecks:    s.arrayEqualChecks,
		MultisetEqualChecks: s.multisetEqualChecks,
		MoveProbes:          s.moveProbes,
		CopyProbes:          s.copyProbes,
		HashCollisions:      s.hashCollisions,
	}
}

// totalEqualChecks returns the overall number of old/new
// equality checks performed while matching arrays and
// discovering move/copy candidates.
func (s Stats) TotalEqualChecks() int {
	return s.ArrayEqualChecks + s.MultisetEqualChecks +
		s.MoveProbes + s.CopyProbes
}

// equalResult caches the outcome of an equality probe.
type equalResult int8

const (
	eqUnknown equalResult = iota
	eqFalse
	eqTrue
)

// arrayMatcher owns the responsibilities involved in matching
// the elements of two arrays: hashing for bucketing, equality
// evaluation, memoized LCS computation and move/copy candidate
// accounting. A matcher is created once per comparison and is
// shared by every nested array comparison of that run, which
// guarantees that every candidate pair is evaluated once.
type arrayMatcher struct {
	digest func(any, bool) uint64
	stats  *compareStats

	// lcsMemo memoizes equality results for element pairs,
	// keyed by i*len(tgt)+j, for the currently matched array.
	lcsMemo []equalResult
	lcsCols int

	// removeCandidates lists, in stable emission order, the
	// digest buckets of remove operations that are still live
	// in the patch and can therefore be turned into moves.
	removeCandidates []removeCandidate
	// unchangedLocations maps value digests to unchanged
	// source locations that can be used as copy sources.
	unchangedLocations map[uint64][]jsonNode
}

type removeCandidate struct {
	patchIdx int
	digest   uint64
	value    any
}

func newArrayMatcher(d *Differ) *arrayMatcher {
	return &arrayMatcher{
		digest: func(v any, sort bool) uint64 {
			return d.hasher.digest(v, sort)
		},
		stats: &d.stats,
	}
}

// digestOf computes the digest of a value once; callers never
// hash the same candidate more than once.
func (m *arrayMatcher) digestOf(v any) uint64 {
	return m.digest(v, false)
}

// equal evaluates the equality of two array elements, counting
// the probe and memoizing its result per (array, pair).
func (m *arrayMatcher) equal(a, b any, i, j int) bool {
	if i >= 0 && j >= 0 && m.lcsMemo != nil {
		k := i*m.lcsCols + j
		switch m.lcsMemo[k] {
		case eqTrue:
			return true
		case eqFalse:
			return false
		}
		m.stats.arrayEqualChecks++
		eq := deepEqual(a, b)
		if eq {
			m.lcsMemo[k] = eqTrue
		} else {
			m.lcsMemo[k] = eqFalse
		}
		return eq
	}
	m.stats.arrayEqualChecks++
	return deepEqual(a, b)
}

// lcsPairs returns the index pairs of the longest common
// subsequence of src and tgt. Equality is delegated to the
// matcher, so every pair of elements is compared at most once.
func (m *arrayMatcher) lcsPairs(src, tgt []any) [][2]int {
	n, p := len(src), len(tgt)

	// Allocate the memoization table for this array.
	m.lcsMemo = make([]equalResult, n*p)
	m.lcsCols = p
	defer func() {
		m.lcsMemo = nil
		m.lcsCols = 0
	}()

	tab := make([][]int, n+1)
	for i := range tab {
		tab[i] = make([]int, p+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= p; j++ {
			if m.equal(src[i-1], tgt[j-1], i-1, j-1) {
				tab[i][j] = tab[i-1][j-1] + 1
			} else {
				tab[i][j] = max(tab[i-1][j], tab[i][j-1])
			}
		}
	}
	i, j := n, p
	pairs := make([][2]int, 0, tab[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.equal(src[i-1], tgt[j-1], i-1, j-1):
			pairs = append(pairs, [2]int{i - 1, j - 1})
			i--
			j--
		case tab[i-1][j] > tab[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i, j := 0, len(pairs)-1; i < j; i, j = i+1, j-1 {
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}
	return pairs
}

// multisetEqual reports whether src and tgt contain the same
// elements regardless of their order. Unlike a bare hash set,
// it handles duplicate values (multiset semantics) and never
// trusts a digest alone: every candidate is verified with a
// real equality check, so hash collisions cannot pair unequal
// elements together.
func (m *arrayMatcher) multisetEqual(src, tgt []any, equivalent bool) bool {
	if len(src) != len(tgt) {
		return false
	}

	type bucketEntry struct {
		value any
		count int
	}
	buckets := make(map[uint64][]bucketEntry, len(src))

	for _, v := range src {
		k := m.digest(v, equivalent)
		entries := buckets[k]
		found := false
		for e := range entries {
			m.stats.multisetEqualChecks++
			if deepEqual(entries[e].value, v) {
				entries[e].count++
				found = true
				break
			}
			m.stats.hashCollisions++
		}
		if !found {
			buckets[k] = append(entries, bucketEntry{value: v, count: 1})
		}
	}
	for _, v := range tgt {
		k := m.digest(v, equivalent)
		entries := buckets[k]
		matched := -1
		for e := range entries {
			m.stats.multisetEqualChecks++
			if deepEqual(entries[e].value, v) {
				matched = e
				break
			}
			m.stats.hashCollisions++
		}
		if matched == -1 {
			return false
		}
		entries[matched].count--
		if entries[matched].count == 0 {
			buckets[k] = append(entries[:matched], entries[matched+1:]...)
		}
	}
	for _, entries := range buckets {
		if len(entries) != 0 {
			return false
		}
	}
	return true
}

// addRemoveCandidate registers a freshly emitted remove
// operation as a potential move source. Candidates stay in
// stable emission order, which is the order in which the
// consuming patch operations see them.
func (m *arrayMatcher) addRemoveCandidate(patchIdx int, v any) {
	if m.removeCandidates == nil {
		m.removeCandidates = make([]removeCandidate, 0, 4)
	}
	m.removeCandidates = append(m.removeCandidates, removeCandidate{
		patchIdx: patchIdx,
		digest:   m.digestOf(v),
		value:    v,
	})
}

// invalidateCandidatesAfter drops every live remove candidate
// located at or after patchIdx. It is invoked when patch
// operations are rewritten (rationalization), after which the
// stale indices would no longer address a live remove.
func (m *arrayMatcher) invalidateCandidatesAfter(patchIdx int) {
	cut := len(m.removeCandidates)
	for i, c := range m.removeCandidates {
		if c.patchIdx >= patchIdx {
			cut = i
			break
		}
	}
	m.removeCandidates = m.removeCandidates[:cut]
}

// findRemoveCandidate returns the patch index of the first
// live remove operation, in stable emission order, whose value
// equals v. The digest is only used to narrow the search; a
// collision always falls back to a verified equality check.
// It returns -1 when no candidate matches.
func (m *arrayMatcher) findRemoveCandidate(v any) int {
	k := m.digestOf(v)
	for _, c := range m.removeCandidates {
		if c.digest != k {
			continue
		}
		m.stats.moveProbes++
		if deepEqual(c.value, v) {
			return c.patchIdx
		}
		m.stats.hashCollisions++
	}
	// Digest collision fallback: a remove with a different
	// digest can never be equal to v (equal values hash alike),
	// so nothing more has to be probed here.
	return -1
}

// consumeRemoveCandidate removes the candidate located at the
// given patch index, because it has been turned into a move.
// Subsequent candidate indices are shifted down by one to stay
// aligned with the patch.
func (m *arrayMatcher) consumeRemoveCandidate(patchIdx int) {
	idx := -1
	for i, c := range m.removeCandidates {
		if c.patchIdx == patchIdx {
			idx = i
			break
		}
	}
	m.removeCandidates = append(m.removeCandidates[:idx], m.removeCandidates[idx+1:]...)
	for i := range m.removeCandidates {
		if m.removeCandidates[i].patchIdx > patchIdx {
			m.removeCandidates[i].patchIdx--
		}
	}
}

// rememberUnchanged records an unchanged source location
// addressable by its value. Traversal order is deterministic,
// and digest collisions are kept distinct and verified on
// lookup instead of being treated as equal.
func (m *arrayMatcher) rememberUnchanged(ptr string, v any) {
	if m.unchangedLocations == nil {
		m.unchangedLocations = make(map[uint64][]jsonNode)
	}
	k := m.digestOf(v)
	nodes := m.unchangedLocations[k]
	m.unchangedLocations[k] = append(nodes, jsonNode{ptr: ptr, val: v})
}

// findUnchanged returns the path of the last recorded
// unchanged location whose value equals v, or "" when none
// exists. Collisions are verified with an equality check.
func (m *arrayMatcher) findUnchanged(v any) string {
	if m.unchangedLocations == nil {
		return emptyPointer
	}
	k := m.digestOf(v)
	nodes := m.unchangedLocations[k]
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		m.stats.copyProbes++
		if deepEqual(n.val, v) {
			return n.ptr
		}
		m.stats.hashCollisions++
	}
	return emptyPointer
}

// shiftCandidatesFrom shifts the patch index of every live
// candidate located at or after from by delta. It keeps the
// candidate index aligned with the patch when an operation is
// inserted in the middle of it (LCS moves).
func (m *arrayMatcher) shiftCandidatesFrom(from, delta int) {
	for i := range m.removeCandidates {
		if m.removeCandidates[i].patchIdx >= from {
			m.removeCandidates[i].patchIdx += delta
		}
	}
}
