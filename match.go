package jsondiff

import "reflect"

// matcherStats holds diagnostic counters that quantify the
// amount of work performed while matching array elements.
//
// The counters are purely diagnostic: they never influence
// the generated patch. Each [Differ] owns its own instance,
// which is reset at every comparison, so counters are
// naturally isolated across concurrent comparisons.
type matcherStats struct {
	// PairComparisons is the number of distinct old/new
	// element pairs whose equality has been determined.
	// Each pair is evaluated at most once; subsequent
	// requests are served from the equality memo.
	PairComparisons int

	// DeepEqualCalls is the number of pairs for which a
	// full recursive equality evaluation was required,
	// i.e. pairs that could not be dismissed by hashes.
	DeepEqualCalls int

	// HashDigests is the number of old/new top-level
	// element digests computed during matching. An
	// element digest is computed at most once and then
	// reused by LCS, multiset matching and move/copy
	// discovery.
	HashDigests int

	// CandidateChecks is the number of recursive
	// equality evaluations performed while discovering
	// move/copy candidates among removed or unchanged
	// values.
	CandidateChecks int
}

// matcherHooks is a package-internal test seam that allows
// the diagnostic test-suite to drive the production matcher
// through otherwise unreachable conditions (mid-run failures
// and hash collisions). Its zero value is the production
// behavior; it is never set by any exported option.
type matcherHooks struct {
	// failAfterPair, when positive, causes the next
	// comparison that reaches the given number of
	// distinct pair comparisons to fail once with a
	// matcherFault. The hook disarms itself, so an
	// immediate retry runs to completion.
	failAfterPair int

	// forceCollide forces every element digest to the
	// same constant value, simulating systematic hash
	// collisions. Equality is still decided by recursive
	// comparison, never by the digest.
	forceCollide bool
}

func (h *matcherHooks) probe(stats *matcherStats) {
	if h == nil || h.failAfterPair <= 0 {
		return
	}
	if stats.PairComparisons >= h.failAfterPair {
		h.failAfterPair = 0 // one-shot: retrying succeeds
		panic(matcherFault{at: stats.PairComparisons})
	}
}

// matcherFault is the package-internal sentinel used to
// report a recoverable, diagnostic array-matching failure.
type matcherFault struct {
	at int
}

func (matcherFault) Error() string {
	return "jsondiff: array matching interrupted"
}

// collidingDigest is the constant digest returned when
// collision simulation is enabled through the test seam.
const collidingDigest uint64 = 0x51a511c011111111

// arrayMatcher unifies the four responsibilities involved
// in matching the elements of two arrays:
//
//   - element equality (with memoization, one result per pair)
//   - element hashing (one digest per element, used only as
//     a negative filter; a digest never proves equality)
//   - longest common subsequence computation
//   - unordered (multiset) equality
//
// The same machinery is consumed by the LCS comparison path,
// the Equivalent option and the move/copy candidate discovery;
// no parallel implementation is maintained for tests.
type arrayMatcher struct {
	src []interface{}
	tgt []interface{}

	// unordered makes nested arrays compare as multisets.
	unordered bool

	// Tri-state equality memo:
	//   0 = not computed, 1 = equal, 2 = unequal
	eq [][]byte

	srcDigests    []uint64
	tgtDigests    []uint64
	srcDigestSeen []bool
	tgtDigestSeen []bool

	h     hasher
	stats *matcherStats
	hooks *matcherHooks
}

// newArrayMatcher builds a matcher whose hasher shares the
// seed of the Differ's hasher, so digests computed here are
// interchangeable with those of the factorization indexes.
func (d *Differ) newArrayMatcher(src, tgt []interface{}, unordered bool) *arrayMatcher {
	m := &arrayMatcher{
		src:       src,
		tgt:       tgt,
		unordered: unordered,
		eq:        make([][]byte, len(src)),
		stats:     &d.stats,
		hooks:     &d.hooks,
	}
	for i := range m.eq {
		m.eq[i] = make([]byte, len(tgt))
	}
	m.h.mh.SetSeed(d.hasher.mh.Seed())
	if len(src) != 0 {
		m.srcDigests = make([]uint64, len(src))
		m.srcDigestSeen = make([]bool, len(src))
	}
	if len(tgt) != 0 {
		m.tgtDigests = make([]uint64, len(tgt))
		m.tgtDigestSeen = make([]bool, len(tgt))
	}
	return m
}

// child returns a matcher for nested arrays, sharing the
// parent seed, stats and hooks.
func (m *arrayMatcher) child(src, tgt []interface{}) *arrayMatcher {
	c := &arrayMatcher{
		src:       src,
		tgt:       tgt,
		unordered: m.unordered,
		eq:        make([][]byte, len(src)),
		stats:     m.stats,
		hooks:     m.hooks,
	}
	for i := range c.eq {
		c.eq[i] = make([]byte, len(tgt))
	}
	c.h.mh.SetSeed(m.h.mh.Seed())
	if len(src) != 0 {
		c.srcDigests = make([]uint64, len(src))
		c.srcDigestSeen = make([]bool, len(src))
	}
	if len(tgt) != 0 {
		c.tgtDigests = make([]uint64, len(tgt))
		c.tgtDigestSeen = make([]bool, len(tgt))
	}
	return c
}

// equal returns whether src[i] and tgt[j] are deeply equal.
// The result is memoized: a given pair is evaluated once.
// Digests are only used as a negative pre-filter; when they
// collide, recursive equality remains the sole authority.
func (m *arrayMatcher) equal(i, j int) bool {
	switch m.eq[i][j] {
	case 1:
		return true
	case 2:
		return false
	}
	if m.stats != nil {
		m.stats.PairComparisons++
		m.hooks.probe(m.stats)
	}

	eq := true
	if m.srcDigest(i) != m.tgtDigest(j) {
		// Different digests guarantee unequal values.
		eq = false
	} else {
		// Digests agree: this may be a genuine match or a
		// collision, equality must always be confirmed.
		if m.stats != nil {
			m.stats.DeepEqualCalls++
		}
		eq = m.valuesEqual(m.src[i], m.tgt[j])
	}
	if eq {
		m.eq[i][j] = 1
	} else {
		m.eq[i][j] = 2
	}
	return eq
}

// valuesEqual compares two JSON values, treating nested
// arrays as multisets when the matcher operates in unordered
// mode.
func (m *arrayMatcher) valuesEqual(a, b interface{}) bool {
	if !m.unordered {
		return deepEqualValue(a, b)
	}
	// Unordered comparisons may be exercised directly with
	// values that were not JSON round-tripped (for instance
	// Go ints). Non-JSON types are decided by deterministic
	// structural equality; the ordered LCS path keeps using
	// deepEqualValue and its error model.
	if jsonTypeSwitch(a) == jsonInvalid || jsonTypeSwitch(b) == jsonInvalid {
		return reflect.DeepEqual(a, b)
	}
	switch aa := a.(type) {
	case []interface{}:
		bb, ok := b.([]interface{})
		if !ok {
			return false
		}
		return m.child(aa, bb).multisetEqual()
	case map[string]interface{}:
		bb, ok := b.(map[string]interface{})
		if !ok || len(aa) != len(bb) {
			return false
		}
		for k, va := range aa {
			vb, present := bb[k]
			if !present || !m.valuesEqual(va, vb) {
				return false
			}
		}
		return true
	default:
		return deepEqualValue(a, b)
	}
}

func (m *arrayMatcher) srcDigest(i int) uint64 {
	if !m.srcDigestSeen[i] {
		m.srcDigests[i] = m.digest(m.src[i])
		m.srcDigestSeen[i] = true
	}
	return m.srcDigests[i]
}

func (m *arrayMatcher) tgtDigest(j int) uint64 {
	if !m.tgtDigestSeen[j] {
		m.tgtDigests[j] = m.digest(m.tgt[j])
		m.tgtDigestSeen[j] = true
	}
	return m.tgtDigests[j]
}

func (m *arrayMatcher) digest(v interface{}) uint64 {
	if m.stats != nil {
		m.stats.HashDigests++
	}
	if m.hooks != nil && m.hooks.forceCollide {
		return collidingDigest
	}
	return m.h.digest(v, m.unordered)
}

// lcs computes the index pairs of the longest common
// subsequence using memoized element equality.
func (m *arrayMatcher) lcs() [][2]int {
	ns, nt := len(m.src), len(m.tgt)
	tab := make([][]int, ns+1)
	for i := 0; i <= ns; i++ {
		tab[i] = make([]int, nt+1)
	}
	for i := 1; i < len(tab); i++ {
		for j := 1; j < len(tab[i]); j++ {
			if m.equal(i-1, j-1) {
				tab[i][j] = tab[i-1][j-1] + 1
			} else {
				tab[i][j] = max(tab[i-1][j], tab[i][j-1])
			}
		}
	}

	i, j := ns, nt
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

// multisetEqual reports whether both arrays contain the
// same elements with the same multiplicities, regardless
// of their order. Digest buckets narrow the search, but
// collisions and multiplicities are resolved by equality.
func (m *arrayMatcher) multisetEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	if len(m.src) == 0 {
		return true
	}
	buckets := make(map[uint64][]int, len(m.tgt))
	for j := range m.tgt {
		k := m.tgtDigest(j)
		buckets[k] = append(buckets[k], j)
	}
	used := make([]bool, len(m.tgt))
	for i := range m.src {
		k := m.srcDigest(i)
		found := -1
		for _, j := range buckets[k] {
			if !used[j] && m.equal(i, j) {
				found = j
				break
			}
		}
		if found == -1 {
			return false
		}
		used[found] = true
	}
	return true
}

// withMatcherHooks is a package-internal option used only by
// the diagnostic test-suite to configure the matcher test seam.
func withMatcherHooks(h matcherHooks) Option {
	return func(d *Differ) {
		d.hooks = h
	}
}
