package jsondiff

// matchStats holds diagnostic counters produced while
// matching arrays and discovering move/copy candidates.
// The counters are only meaningful for the duration of a
// single comparison; they are never consulted by the diff
// algorithm to make decisions and do not affect the
// generated patch.
type matchStats struct {
	// Comparisons is the number of distinct old/new value
	// pairs for which the canonical equality predicate
	// (deepEqual) has been evaluated during LCS matching
	// and move/copy candidate resolution. Each candidate
	// pair is therefore counted at most once.
	Comparisons int
	// HashDigests is the number of value digests computed.
	// Hashes are only used to narrow candidate sets; they
	// never establish equality on their own.
	HashDigests int
	// HashVerifications counts equality checks performed
	// after a hash lookup, to reject digest collisions.
	HashVerifications int
	// MoveCandidates counts removed-element lookups done
	// while resolving a potential move operation.
	MoveCandidates int
	// CopyCandidates counts unchanged-location lookups
	// done while resolving a potential copy operation.
	CopyCandidates int
}

// matchEngine owns the per-comparison state shared by array
// matching and move/copy discovery: the hasher, the
// diagnostic counters and the memoization tables that
// guarantee an equality between two array elements is
// evaluated at most once for the whole comparison.
//
// A matchEngine is created fresh at the beginning of every
// comparison and discarded when it ends, so concurrent
// comparisons never share hasher state, caches or counters.
type matchEngine struct {
	hasher    hasher
	stats     matchStats
	slotVals  []interface{}
	slotEqual map[[2]int]bool

	// The following fields are test seams. They are nil in
	// production and exist solely to let the in-package
	// behavior-guard tests exercise recovery and digest
	// collisions through the public entry points. They do
	// not alter results when unset.
	probeHook func(probeCount int)
	yieldHook func(probeCount int)
	hashOf    func(v interface{}) (uint64, bool)
}

func newMatchEngine(seams testSeams) *matchEngine {
	return &matchEngine{
		slotEqual: make(map[[2]int]bool),
		probeHook: seams.probeHook,
		yieldHook: seams.yieldHook,
		hashOf:    seams.hashOf,
	}
}

// testSeams groups the unexported, test-only hooks that may
// be installed on a Differ. Production code never sets them.
type testSeams struct {
	probeHook func(probeCount int)
	yieldHook func(probeCount int)
	hashOf    func(v interface{}) (uint64, bool)
}

// registerSlot assigns a comparison-unique identifier to an
// array element and returns it. Identifiers are used to
// memoize equality between elements of different arrays,
// including cross-array move/copy candidates.
func (e *matchEngine) registerSlot(v interface{}) int {
	id := len(e.slotVals)
	e.slotVals = append(e.slotVals, v)
	return id
}

// slotsEqual reports whether the elements identified by the
// two slot identifiers are deeply equal. The result is
// memoized for the whole comparison, so a given candidate
// pair is evaluated by the canonical equality predicate
// exactly once.
func (e *matchEngine) slotsEqual(a, b int) bool {
	if a == b {
		return true
	}
	key := [2]int{a, b}
	if key[0] > key[1] {
		key[0], key[1] = key[1], key[0]
	}
	if eq, ok := e.slotEqual[key]; ok {
		return eq
	}
	if e.probeHook != nil {
		e.probeHook(e.stats.Comparisons)
	}
	if e.yieldHook != nil {
		e.yieldHook(e.stats.Comparisons)
	}
	e.stats.Comparisons++
	eq := deepEqual(e.slotVals[a], e.slotVals[b])
	e.slotEqual[key] = eq
	return eq
}

// valuesEqual resolves equality for two values that may not
// belong to a registered matcher (for example an element
// removed outside of LCS matching). It registers both
// values on the fly so that the result remains memoized.
func (e *matchEngine) valuesEqual(a, b interface{}) bool {
	return e.slotsEqual(e.registerSlot(a), e.registerSlot(b))
}

// digest computes the hash of a value. When a test seam
// overrides the digest, the override is used; otherwise the
// regular hasher is used. Hashes only narrow candidate sets
// and are always confirmed by the equality predicate.
func (e *matchEngine) digest(v interface{}, sort bool) uint64 {
	e.stats.HashDigests++
	if e.hashOf != nil {
		if h, ok := e.hashOf(v); ok {
			return h
		}
	}
	return e.hasher.digest(v, sort)
}

// arrayMatcher consolidates, for a single pair of arrays,
// the four responsibilities that used to be scattered across
// the code: equality, hashing, longest common subsequence
// computation and move/copy candidate discovery.
//
// Equality results used by the LCS table are memoized in
// eqGrid and shared with the parent matchEngine, so that the
// later move/copy discovery phase reuses them instead of
// re-evaluating candidates.
type arrayMatcher struct {
	engine   *matchEngine
	src, tgt []interface{}
	oldSlots []int
	newSlots []int
	eqGrid   [][]int8 // 0 unknown, 1 equal, -1 not equal
}

const (
	eqUnknown int8 = 0
	eqEqual   int8 = 1
	eqDiffer  int8 = -1
)

func newArrayMatcher(e *matchEngine, src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		engine:   e,
		src:      src,
		tgt:      tgt,
		oldSlots: make([]int, len(src)),
		newSlots: make([]int, len(tgt)),
		eqGrid:   make([][]int8, len(src)),
	}
	for i := range src {
		m.oldSlots[i] = e.registerSlot(src[i])
	}
	for i := range tgt {
		m.newSlots[i] = e.registerSlot(tgt[i])
	}
	for i := range src {
		m.eqGrid[i] = make([]int8, len(tgt))
	}
	return m
}

// equal reports whether src[i] deeply equals tgt[j]. The
// predicate is evaluated once per cell and cached.
func (m *arrayMatcher) equal(i, j int) bool {
	if v := m.eqGrid[i][j]; v != eqUnknown {
		return v == eqEqual
	}
	eq := m.engine.slotsEqual(m.oldSlots[i], m.newSlots[j])
	if eq {
		m.eqGrid[i][j] = eqEqual
	} else {
		m.eqGrid[i][j] = eqDiffer
	}
	return eq
}

// lcsPairs computes the index pairs of the longest common
// subsequence using the memoized equality grid.
func (m *arrayMatcher) lcsPairs() [][2]int {
	n, p := len(m.src), len(m.tgt)
	tab := make([][]int, n+1)
	for i := range tab {
		tab[i] = make([]int, p+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= p; j++ {
			if m.equal(i-1, j-1) {
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
		case m.equal(i-1, j-1):
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

// unorderedEqual reports whether the two arrays contain the
// same multiset of elements regardless of their order. It is
// used by the Equivalent option.
//
// Elements are grouped by digest, and equality is only
// established by the canonical equality predicate, so digest
// collisions can never pair distinct duplicates incorrectly.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	type item struct {
		slot int
		used bool
	}
	buckets := make(map[uint64][]item, len(m.tgt))
	for j, v := range m.tgt {
		h := m.engine.digest(v, true)
		buckets[h] = append(buckets[h], item{slot: m.newSlots[j]})
	}
	for i := range m.src {
		h := m.engine.digest(m.src[i], true)
		matched := false
		for k := range buckets[h] {
			it := &buckets[h][k]
			if it.used {
				continue
			}
			if m.engine.slotsEqual(m.oldSlots[i], it.slot) {
				it.used = true
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
