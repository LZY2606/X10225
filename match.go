package jsondiff

import "reflect"

// This file unifies the responsibilities involved while
// diffing two JSON arrays:
//
//   - equality: every (src[i], tgt[j]) pair is compared at
//     most once, through a memoized predicate;
//   - hashing: every element digest is computed at most
//     once, and hashes are only used as grouping hints,
//     never as a proof of equality;
//   - longest common subsequence: the LCS table is derived
//     from the memoized equality matrix, so its backtracking
//     phase performs no additional equality comparison;
//   - move/copy discovery: candidates removed from the
//     source document are registered once and probed for
//     each inserted value at most once per candidate, even
//     in presence of duplicate values or hash collisions.
//
// The state lives for a single top-level comparison only;
// it is never shared between [Differ] instances, which makes
// concurrent comparisons independent from each other.

// matchCounters holds diagnostic counters about the work
// performed by the array matching machinery during one
// comparison. Counters are diagnostic only: they never
// influence the generated patch.
type matchCounters struct {
	// equalComparisons counts invocations of the deep
	// equality predicate performed while matching arrays
	// (LCS matrix, multiset equivalence and collision
	// verification on shared unchanged values).
	equalComparisons int
	// hashDigests counts element digest computations
	// performed while matching arrays.
	hashDigests int
	// lcsMatrices counts LCS dynamic-programming tables
	// that have been computed.
	lcsMatrices int
	// moveCandidateTests counts equality verifications
	// between an inserted value and a removed candidate.
	moveCandidateTests int
}

// matchDiagnostics is a point-in-time snapshot of the
// internal matching counters. It is intentionally
// unexported and used only by the package's own tests to
// assert on the JSON diff/array matching semantics; the
// snapshot is produced from the exact same code path as
// production comparisons, there is no test-only algorithm.
type matchDiagnostics struct {
	EqualComparisons   int
	HashDigests        int
	LCSMatrices        int
	MoveCandidateTests int
}

func (c matchCounters) snapshot() matchDiagnostics {
	return matchDiagnostics{
		EqualComparisons:   c.equalComparisons,
		HashDigests:        c.hashDigests,
		LCSMatrices:        c.lcsMatrices,
		MoveCandidateTests: c.moveCandidateTests,
	}
}

// moveCandidate describes a value that has been removed
// from the source document and that may therefore satisfy
// a later addition as a move operation.
type moveCandidate struct {
	id     int
	path   string
	value  interface{}
	digest uint64
	used   bool
}

// arrayMatcher owns every piece of state used while
// matching arrays during one comparison.
type arrayMatcher struct {
	d *Differ

	// Per-array memoized equality predicate, keyed by
	// i*len(tgt)+j. A value of 1 means equal, 2 unequal;
	// 0 means not computed yet.
	eq   []uint8
	srcN int
	tgtN int

	// Removed values that may be reused by move operations.
	candidates []*moveCandidate
	byHash     map[uint64][]*moveCandidate
	// rejected records candidate ids that already failed
	// the equality test against a given inserted digest.
	rejected map[uint64]map[int]struct{}
}

func newArrayMatcher(d *Differ) *arrayMatcher {
	return &arrayMatcher{
		d:        d,
		byHash:   make(map[uint64][]*moveCandidate),
		rejected: make(map[uint64]map[int]struct{}),
	}
}

// beginArray resets the per-array memoization tables. The
// cross-array move candidate registry is preserved.
func (m *arrayMatcher) beginArray(src, tgt []interface{}) {
	n, k := len(src), len(tgt)
	if cap(m.eq) < n*k {
		m.eq = make([]uint8, n*k)
	} else {
		m.eq = m.eq[:n*k]
		for i := range m.eq {
			m.eq[i] = 0
		}
	}
	m.srcN, m.tgtN = n, k
}

// equal reports whether src[i] and tgt[j] are deeply equal,
// memoizing the result so that each pair is evaluated once.
func (m *arrayMatcher) equal(src, tgt []interface{}, i, j int) bool {
	idx := i*m.tgtN + j
	switch m.eq[idx] {
	case 1:
		return true
	case 2:
		return false
	}
	m.d.matchCounters.equalComparisons++
	if deepEqual(src[i], tgt[j]) {
		m.eq[idx] = 1
		return true
	}
	m.eq[idx] = 2
	return false
}

// registerRemoved records a value removed from the source
// document as a move candidate for future insertions.
func (m *arrayMatcher) registerRemoved(path string, v interface{}) {
	m.d.matchCounters.hashDigests++
	h := m.d.digest(v, false)
	c := &moveCandidate{
		id:     len(m.candidates),
		path:   path,
		value:  v,
		digest: h,
	}
	m.candidates = append(m.candidates, c)
	m.byHash[h] = append(m.byHash[h], c)
}

// takeMoveCandidate looks for a removed value deeply equal
// to v, in the order in which removals were registered.
// Each (candidate, inserted digest) pair is verified at
// most once, and a returned candidate is consumed so it
// can never satisfy another insertion.
func (m *arrayMatcher) takeMoveCandidate(v interface{}, digest uint64) *moveCandidate {
	bucket := m.byHash[digest]
	if len(bucket) == 0 {
		return nil
	}
	rej := m.rejected[digest]
	for _, c := range bucket {
		if c.used || c.digest != digest {
			continue
		}
		if rej != nil {
			if _, ok := rej[c.id]; ok {
				continue
			}
		}
		m.d.matchCounters.moveCandidateTests++
		if deepEqual(c.value, v) {
			c.used = true
			return c
		}
		if rej == nil {
			rej = make(map[int]struct{})
			m.rejected[digest] = rej
		}
		rej[c.id] = struct{}{}
	}
	return nil
}

// lcsPairs computes the index pairs of the longest common
// subsequence between the current arrays, using the
// memoized equality matrix. Tie-breaking is deterministic
// and matches the historical implementation: when the two
// predecessor cells are equal, the target arrow advances
// first, which keeps the public patch order stable.
func (m *arrayMatcher) lcsPairs(src, tgt []interface{}) [][2]int {
	m.d.matchCounters.lcsMatrices++

	n, k := len(src), len(tgt)
	tab := make([]int, (n+1)*(k+1))
	at := func(i, j int) int {
		return tab[i*(k+1)+j]
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= k; j++ {
			if m.equal(src, tgt, i-1, j-1) {
				tab[i*(k+1)+j] = at(i-1, j-1) + 1
			} else {
				tab[i*(k+1)+j] = max(at(i-1, j), at(i, j-1))
			}
		}
	}
	i, j := n, k
	s := make([][2]int, 0, at(n, k))
	for i > 0 && j > 0 {
		switch {
		case m.equal(src, tgt, i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case at(i-1, j) > at(i, j-1):
			i--
		default:
			j--
		}
	}
	for x, y := 0, len(s)-1; x < y; x, y = x+1, y-1 {
		s[x], s[y] = s[y], s[x]
	}
	return s
}

// unorderedEqual reports whether the two arrays contain
// the same multiset of values. Values are grouped by their
// digest first and compared with deep equality, so that
// duplicate values are matched by multiplicity and a hash
// collision can never produce a false positive.
//
// It manages its own digest memoization because the
// equivalence check may run on working copies of the input
// arrays and is conceptually independent of the LCS
// per-array tables.
func (m *arrayMatcher) unorderedEqual(src, tgt []interface{}) bool {
	n, k := len(src), len(tgt)
	if n != k {
		return false
	}
	srcHashes := make([]uint64, n)
	tgtHashes := make([]uint64, k)
	srcKnown := make([]bool, n)
	tgtKnown := make([]bool, k)

	srcDigest := func(i int) uint64 {
		if !srcKnown[i] {
			m.d.matchCounters.hashDigests++
			srcHashes[i] = m.d.digest(src[i], false)
			srcKnown[i] = true
		}
		return srcHashes[i]
	}
	tgtDigest := func(j int) uint64 {
		if !tgtKnown[j] {
			m.d.matchCounters.hashDigests++
			tgtHashes[j] = m.d.digest(tgt[j], false)
			tgtKnown[j] = true
		}
		return tgtHashes[j]
	}

	used := make([]bool, n)
	for j := 0; j < k; j++ {
		hd := tgtDigest(j)
		matched := false
		for i := 0; i < n; i++ {
			if used[i] {
				continue
			}
			if srcDigest(i) != hd {
				continue
			}
			m.d.matchCounters.equalComparisons++
			if m.equalQuiet(src[i], tgt[j]) {
				used[i] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// Stable checkpoints at which the internal matching hook
// may interrupt an array comparison. Ordering is fixed so
// that injected failures are reproducible.
const (
	matchStageEquiv = "equiv"
	matchStageLCS   = "lcs"
	matchStageEmit  = "emit"
)

// errAbortedMatch wraps the error returned by an internal
// matching hook when a comparison is deliberately aborted.
type errAbortedMatch struct {
	err error
}

func (e errAbortedMatch) Error() string {
	return "jsondiff: array comparison aborted: " + e.err.Error()
}

func (e errAbortedMatch) Unwrap() error {
	return e.err
}

// cloneJSONSlice returns a shallow copy of a so the
// unordered-equivalence sort cannot mutate the caller's
// arrays.
func cloneJSONSlice(a []interface{}) []interface{} {
	c := make([]interface{}, len(a))
	copy(c, a)
	return c
}

// equalQuiet compares two values with the same deep
// equality predicate used everywhere else, but when the
// values carry a type that cannot originate from a JSON
// document it falls back to Go's native reflection
// comparison. This only backs the unordered-equivalence
// heuristic, whose historical implementation was purely
// hash-based and never validated value types; the regular
// array matching pipeline still surfaces the
// [invalidJSONTypeError] through the public entry points,
// preserving the existing error model.
func (m *arrayMatcher) equalQuiet(a, b interface{}) (eq bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(invalidJSONTypeError); ok {
				eq = reflect.DeepEqual(a, b)
				return
			}
			panic(r)
		}
	}()
	return deepEqual(a, b)
}
