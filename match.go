package jsondiff

import "reflect"

// matchPhase identifies one stage of the array matching
// pipeline. It is used solely by the diagnostic seam.
type matchPhase uint8

const (
	phaseUnorderedEqual matchPhase = iota + 1
	phaseLCS
)

// MatchCounters records the number of source-vs-target
// equality comparisons performed while matching JSON
// arrays. The counters exist solely for diagnostic
// purposes: they are never consulted to produce a patch,
// and therefore cannot influence its structure or order.
//
// A fresh set of counters is created for every top-level
// comparison, so concurrent comparisons performed on
// distinct Differ instances never share counters.
type MatchCounters struct {
	// EqualPairs is the number of top-level comparisons
	// between an old (source) and a new (target) value
	// performed while evaluating unordered equivalence or
	// filling/backtracking the LCS table.
	EqualPairs int

	// CandidatePairs is the number of top-level
	// source/target comparisons performed while probing
	// move candidates. It is zero when the Factorize option
	// is disabled, in which case candidate discovery is not
	// performed at all.
	CandidatePairs int
}

// Total returns the total number of old/new equality
// comparisons performed during the last comparison.
func (c MatchCounters) Total() int {
	return c.EqualPairs + c.CandidatePairs
}

// removedCandidate is a value removed from the source
// document that may be reused by a subsequent add as the
// target of a move operation.
type removedCandidate struct {
	path string
	val  interface{}
	hash uint64
	used bool
	// region is the index of the array region the candidate
	// belongs to, or -1 for candidates emitted outside of any
	// array comparison (object removals, root scalars).
	region int
}

// matchRegion isolates the move-candidate state generated
// while comparing a single array. If the move/copy
// factorization of that array cannot be proven applicable,
// the region is rolled back and the array is regenerated
// conservatively without leaking its candidates.
type matchRegion struct {
	prefix       string
	conservative bool
	// base is the number of globally registered candidates
	// present when the region opened.
	base int
	// pending counts candidates registered by this region
	// before it is committed.
	pending int
}

// arrayMatcher centralizes the responsibilities involved
// in matching two JSON arrays:
//
//   - equality: every old/new value pair is verified with
//     deepEqual, a hash digest alone is never accepted as
//     proof of equality;
//   - hashing: digests are used exclusively as a
//     collision-safe pre-filter to bucket candidate values;
//   - the longest common subsequence is computed once, and
//     its per-cell equality results are shared between the
//     dynamic-programming fill and the backtracking walk;
//   - move candidates are discovered once, through the
//     single remove/add choke point, indexed by digest
//     instead of being rescanned for every addition, and
//     isolated per array region so ambiguous pairings can be
//     rejected and retried without side effects.
//
// A new matcher is attached to the Differ at the start of
// every top-level comparison. This isolates memoization,
// counters and temporary candidate state between calls,
// including concurrent calls on distinct Differ instances.
type arrayMatcher struct {
	d *Differ

	buckets map[uint64][]int
	removes []removedCandidate

	regions []matchRegion

	counters MatchCounters
}

func newArrayMatcher(d *Differ) *arrayMatcher {
	return &arrayMatcher{d: d}
}

// beginRegion opens an isolated candidate region for the
// array located at prefix.
func (m *arrayMatcher) beginRegion(prefix string) {
	m.regions = append(m.regions, matchRegion{
		prefix: prefix,
		base:   len(m.removes),
	})
}

// currentRegion returns the innermost array region.
func (m *arrayMatcher) currentRegion() *matchRegion {
	if n := len(m.regions); n > 0 {
		return &m.regions[n-1]
	}
	return nil
}

// commitRegion publishes the region's pending candidates,
// making them visible to enclosing scopes and subsequent
// regions (enabling cross-array/object moves).
func (m *arrayMatcher) commitRegion() {
	if n := len(m.regions); n > 0 {
		m.regions = m.regions[:n-1]
	}
}

// rollbackRegion discards every candidate registered by the
// innermost region and removes it from the digest buckets.
func (m *arrayMatcher) rollbackRegion() {
	region := m.currentRegion()
	if region == nil {
		return
	}
	for i := region.base; i < len(m.removes); i++ {
		h := m.removes[i].hash
		bucket := m.buckets[h]
		kept := bucket[:0]
		for _, candidate := range bucket {
			if candidate >= region.base {
				continue
			}
			kept = append(kept, candidate)
		}
		m.buckets[h] = kept
	}
	m.removes = m.removes[:region.base]
	m.regions = m.regions[:len(m.regions)-1]
}

// digest returns the digest of v, routing through the
// Differ so that the whole pipeline (preparation and
// move-candidate lookup) shares a single hashing entry
// point.
func (m *arrayMatcher) digest(v interface{}, sort bool) uint64 {
	if m.d != nil {
		return m.d.digest(v, sort)
	}
	var h hasher
	return h.digest(v, sort)
}

// equal verifies the structural equality of a source and a
// target value. deepEqual remains the single source of
// truth for value equality on JSON values; this wrapper only
// accounts for the comparison. Values that are not valid
// JSON types can only reach the matcher through its
// standalone entry points (the top-level Compare functions
// always marshal inputs first), and fall back to regular
// reflect equality so that those entry points keep working.
func (m *arrayMatcher) equal(src, tgt interface{}) bool {
	m.counters.EqualPairs++
	if jsonTypeSwitch(src) == jsonInvalid || jsonTypeSwitch(tgt) == jsonInvalid {
		return reflect.DeepEqual(src, tgt)
	}
	return deepEqual(src, tgt)
}

// equalValue compares a registered candidate with a target
// without incrementing the LCS/equivalence comparison
// counter; candidate probes are accounted for separately.
func (m *arrayMatcher) equalValue(src, tgt interface{}) bool {
	if jsonTypeSwitch(src) == jsonInvalid || jsonTypeSwitch(tgt) == jsonInvalid {
		return reflect.DeepEqual(src, tgt)
	}
	return deepEqual(src, tgt)
}

// lcs computes the longest common subsequence of src and
// tgt and returns the matched index pairs, ordered by
// ascending source index.
//
// Each positional pair src[i]/tgt[j] is compared at most
// once: the equality table populated by the dynamic
// programming fill is reused verbatim by the backtracking
// walk, instead of re-running deepEqual on matched cells.
func (m *arrayMatcher) lcs(src, tgt []interface{}) [][2]int {
	n, w := len(src), len(tgt)

	table := make([][]int, n+1)
	eq := make([]bool, (n+1)*(w+1))
	eqAt := func(i, j int) bool { return eq[i*(w+1)+j] }

	for i := 0; i <= n; i++ {
		table[i] = make([]int, w+1)
	}
	for i := 1; i <= n; i++ {
		if m.d != nil && m.d.matchFault != nil && n > 1 && i == n/2+1 {
			// Diagnostic seam: fired exactly once, midway
			// through the LCS fill, while the table is
			// partially populated.
			m.d.matchFault(phaseLCS)
		}
		for j := 1; j <= w; j++ {
			equal := m.equal(src[i-1], tgt[j-1])
			eq[i*(w+1)+j] = equal
			if equal {
				table[i][j] = table[i-1][j-1] + 1
			} else {
				table[i][j] = max(table[i-1][j], table[i][j-1])
			}
		}
	}

	i, j := n, w
	pairs := make([][2]int, 0, table[i][j])

	for i > 0 && j > 0 {
		switch {
		case eqAt(i, j):
			pairs = append(pairs, [2]int{i - 1, j - 1})
			i--
			j--
		case table[i-1][j] > table[i][j-1]:
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

// unorderedEqual reports whether src and tgt contain the
// same multiset of values, regardless of their order.
//
// Digests bucket values and deepEqual disambiguates bucket
// members, so repeated values are accounted for
// individually and two distinct values that share a digest
// can never be mistaken for each other.
func (m *arrayMatcher) unorderedEqual(src, tgt []interface{}) bool {
	if len(src) != len(tgt) {
		return false
	}

	type entry struct {
		val   interface{}
		count int
	}
	buckets := make(map[uint64][]*entry, len(src))

	find := func(bucket []*entry, v interface{}) *entry {
		for _, e := range bucket {
			if m.equal(e.val, v) {
				return e
			}
		}
		return nil
	}

	for _, v := range src {
		h := m.digest(v, true)
		bucket := buckets[h]
		if e := find(bucket, v); e != nil {
			e.count++
		} else {
			buckets[h] = append(bucket, &entry{val: v, count: 1})
		}
	}
	for _, v := range tgt {
		h := m.digest(v, true)
		e := find(buckets[h], v)
		if e == nil || e.count == 0 {
			return false
		}
		e.count--
	}
	return true
}

// registerRemoved records a value removed from the source
// document as a move candidate. Each removal is registered
// exactly once and its digest is computed at that point.
func (m *arrayMatcher) registerRemoved(path string, v interface{}) {
	if m.buckets == nil {
		m.buckets = make(map[uint64][]int)
	}
	h := m.digest(v, false)
	seq := len(m.removes)
	regionIdx := -1
	if region := m.currentRegion(); region != nil {
		regionIdx = len(m.regions) - 1
		region.pending++
	}
	m.removes = append(m.removes, removedCandidate{
		path:   path,
		val:    v,
		hash:   h,
		region: regionIdx,
	})
	m.buckets[h] = append(m.buckets[h], seq)
}

// sameArrayMoveAllowed reports whether the innermost,
// conservative array region permits pairing two of its own
// elements through a move. Conservative regions only allow
// cross-boundary factorization and therefore reject
// same-array moves, which are exactly the pairings that can
// be ambiguous.
func (m *arrayMatcher) sameArrayMoveAllowed(from, path string) bool {
	region := m.currentRegion()
	if region == nil {
		return true
	}
	if !region.conservative {
		return true
	}
	same := hasProperPathPrefix(region.prefix, path) &&
		hasProperPathPrefix(region.prefix, from)
	return !same
}

// findRemoved returns the patch index of the first live
// removed candidate equal to v, in patch emission order. It
// returns -1 when there is no such candidate (or when using
// it as a move is disallowed in the current region) and,
// mirroring the historical behavior, -2 when the only
// matching candidate cannot be moved into its own child
// path.
//
// Candidates are looked up through their digest bucket, so
// a candidate is hashed once when removed and deepEqual is
// invoked at most once per candidate/add probe, never
// across the whole patch.
func (m *arrayMatcher) findRemoved(d *Differ, path string, v interface{}) int {
	const blocked = -2

	h := m.digest(v, false)
	seq := -1

	for _, candidate := range m.buckets[h] {
		c := &m.removes[candidate]
		if c.used {
			continue
		}
		m.counters.CandidatePairs++
		if m.equalValue(c.val, v) {
			seq = candidate
			break
		}
	}
	if seq == -1 {
		return -1
	}
	c := &m.removes[seq]

	// https://tools.ietf.org/html/rfc6902#section-4.4f
	if hasProperPathPrefix(c.path, path) {
		return blocked
	}
	if !m.sameArrayMoveAllowed(c.path, path) {
		return -1
	}
	c.used = true

	// Map the global removal sequence back to its live
	// position inside the patch. Live removes preserve
	// registration order; consumed candidates have been
	// removed from both the patch and the live list, so the
	// k-th unconsumed candidate is the k-th remove op.
	rank := 0
	for candidate := 0; candidate < seq; candidate++ {
		if !m.removes[candidate].used {
			rank++
		}
	}
	live := 0
	for i := 0; i < len(d.patch); i++ {
		if d.patch[i].Type != OperationRemove {
			continue
		}
		if live == rank {
			return i
		}
		live++
	}
	return -1
}

// copySourceAllowed mirrors sameArrayMoveAllowed for copy
// operations: conservative regions reject same-array copies,
// whose source indices shift while the region is applied.
func (m *arrayMatcher) copySourceAllowed(from, path string) bool {
	return m.sameArrayMoveAllowed(from, path)
}
