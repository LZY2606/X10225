package jsondiff

// This file centralizes the responsibilities that participate
// in matching array elements between a source and a target
// document:
//
//   - equality is always established by deepEqualValue; a
//     structural digest is used solely as a collision-free
//     negative prefilter, never as an equality witness;
//   - digests of each side are computed at most once per
//     element;
//   - equality probes for a given (source, target) pair are
//     evaluated at most once and shared by the LCS walk,
//     the unordered-equivalence check and move discovery;
//   - move/copy candidates are discovered through a single
//     ledger that records every remove operation.
//
// The diagnostic counters exposed on arrayMatchStats do not
// participate in any decision: they are populated by the
// production path itself and are reset on every comparison,
// which keeps them isolated between concurrent Differ values.

// arrayMatchStats gathers diagnostic counters that quantify
// the work performed while matching arrays. The counters are
// purely observational: they never influence the generated
// patch.
type arrayMatchStats struct {
	// digestOld/digestNew count the structural digests
	// computed for source and target elements respectively.
	digestOld int
	digestNew int
	// pairsExamined counts old/new element pairs probed
	// through the equality oracle (LCS table, unordered
	// pairing and move discovery).
	pairsExamined int
	// deepEqualProbes counts the pairs for which a full
	// deep equality evaluation was required.
	deepEqualProbes int
	// equalPairs counts the probes that yielded equality.
	equalPairs int
	// moveCandidates counts the remove/add value pairs
	// inspected while looking for a move.
	moveCandidates int
	// moveFound counts the move operations materialized.
	moveFound int
	// copyCandidates counts the unchanged locations
	// inspected while looking for a copy.
	copyCandidates int
	// copyFound counts the copy operations materialized.
	copyFound int
}

func (s arrayMatchStats) equals(o arrayMatchStats) bool {
	return s == o
}

// arrayMatcher owns the matching state for a single pair of
// arrays compared by a Differ. Every equality relation used
// by the LCS computation, the unordered-equivalence shortcut
// and move discovery is resolved through the matcher, so a
// candidate pair is never evaluated twice.
type arrayMatcher struct {
	d    *Differ
	src  []interface{}
	tgt  []interface{}
	stat *arrayMatchStats

	// Per-element digests, lazily computed at most once
	// per element and per ordering mode. Each slot
	// corresponds to the unsorted/sorted ordering mode.
	oldDig  [][2]uint64
	newDig  [][2]uint64
	oldMask []uint8
	newMask []uint8

	// Cache of evaluated (src index, tgt index) pairs.
	eqCache map[[2]int]bool
}

// newArrayMatcher creates the matcher used to compare src
// against tgt. The optional stats pointer is shared across
// nested matchers of a single comparison when diagnostics are
// requested.
func newArrayMatcher(d *Differ, src, tgt []interface{}, stat *arrayMatchStats) *arrayMatcher {
	return &arrayMatcher{
		d:       d,
		src:     src,
		tgt:     tgt,
		stat:    stat,
		oldDig:  make([][2]uint64, len(src)),
		newDig:  make([][2]uint64, len(tgt)),
		oldMask: make([]uint8, len(src)),
		newMask: make([]uint8, len(tgt)),
		eqCache: make(map[[2]int]bool, min(len(src)*len(tgt), 16)),
	}
}

// digestBit returns the cache mask bit associated with an
// ordering mode.
func digestBit(sorted bool) uint8 {
	if sorted {
		return 1 << 1
	}
	return 1 << 0
}

// digestOfOld returns the structural digest of src[i],
// computing it at most once per ordering mode.
func (m *arrayMatcher) digestOfOld(i int, sorted bool) uint64 {
	bit := digestBit(sorted)
	if m.oldMask[i]&bit != 0 {
		if sorted {
			return m.oldDig[i][1]
		}
		return m.oldDig[i][0]
	}
	dig := m.d.digestValue(m.src[i], sorted)
	if sorted {
		m.oldDig[i][1] = dig
	} else {
		m.oldDig[i][0] = dig
	}
	m.oldMask[i] |= bit
	if m.stat != nil {
		m.stat.digestOld++
	}
	return dig
}

// digestOfNew returns the structural digest of tgt[j],
// computing it at most once per ordering mode.
func (m *arrayMatcher) digestOfNew(j int, sorted bool) uint64 {
	bit := digestBit(sorted)
	if m.newMask[j]&bit != 0 {
		if sorted {
			return m.newDig[j][1]
		}
		return m.newDig[j][0]
	}
	dig := m.d.digestValue(m.tgt[j], sorted)
	if sorted {
		m.newDig[j][1] = dig
	} else {
		m.newDig[j][0] = dig
	}
	m.newMask[j] |= bit
	if m.stat != nil {
		m.stat.digestNew++
	}
	return dig
}

// evaluate resolves the equality of the (i, j) old/new pair
// exactly once. A digest mismatch proves inequality without
// invoking deep equality; a digest match still requires a
// full deep equality check because digests are not hashes of
// equality and may collide.
func (m *arrayMatcher) evaluate(i, j int, sorted bool) bool {
	if m.digestOfOld(i, sorted) != m.digestOfNew(j, sorted) {
		return false
	}
	if m.stat != nil {
		m.stat.deepEqualProbes++
	}
	if deepEqual(m.src[i], m.tgt[j]) {
		if m.stat != nil {
			m.stat.equalPairs++
		}
		return true
	}
	return false
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// Every call is counted as a pair examination, while the
// underlying evaluation is memoized.
func (m *arrayMatcher) equal(i, j int, sorted bool) bool {
	key := [2]int{i, j}
	if eq, ok := m.eqCache[key]; ok {
		return eq
	}
	if m.stat != nil {
		m.stat.pairsExamined++
	}
	eq := m.evaluate(i, j, sorted)
	m.eqCache[key] = eq
	return eq
}

// lcsPairs computes the index pairs of the longest common
// subsequence using the memoized equality oracle.
func (m *arrayMatcher) lcsPairs() [][2]int {
	n, p := len(m.src), len(m.tgt)
	table := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		table[i] = make([]int, p+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= p; j++ {
			if m.equal(i-1, j-1, false) {
				table[i][j] = table[i-1][j-1] + 1
			} else {
				table[i][j] = max(table[i-1][j], table[i][j-1])
			}
		}
	}
	i, j := n, p
	pairs := make([][2]int, 0, table[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.equal(i-1, j-1, false):
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

// unorderedEqual reports whether the two arrays contain the
// same multiset of elements regardless of their ordering.
// Digests establish candidate buckets, and duplicate values
// are matched by deep equality one-for-one: a digest match is
// never accepted on its own, so a digest collision cannot pair
// distinct elements.
func (m *arrayMatcher) unorderedEqual() bool {
	n, p := len(m.src), len(m.tgt)
	if n != p {
		return false
	}
	// Buckets index target positions by structural digest,
	// preserving their first-to-last order for stable
	// matching of duplicate values.
	buckets := make(map[uint64][]int, p)
	for j := 0; j < p; j++ {
		h := m.digestOfNew(j, true)
		buckets[h] = append(buckets[h], j)
	}
	used := make(map[int]bool, n)
	for i := 0; i < n; i++ {
		h := m.digestOfOld(i, true)
		candidates := buckets[h]
		matched := -1
		for _, j := range candidates {
			if used[j] {
				continue
			}
			if m.unorderedPairEqual(i, j) {
				matched = j
				break
			}
		}
		if matched == -1 {
			return false
		}
	used[matched] = true
	}
	return true
}

// unorderedPairEqual resolves the equality of a pair during
// the unordered multiset pairing. Values that json.Unmarshal
// cannot produce (invalid JSON types) retain the historical
// hash-only behavior of the equivalence shortcut; every
// regular JSON value goes through the memoized, deep
// equality based oracle.
func (m *arrayMatcher) unorderedPairEqual(i, j int) bool {
	if jsonTypeSwitch(m.src[i]) == jsonInvalid || jsonTypeSwitch(m.tgt[j]) == jsonInvalid {
		if m.stat != nil {
			m.stat.pairsExamined++
		}
		return m.digestOfOld(i, true) == m.digestOfNew(j, true)
	}
	return m.equal(i, j, true)
}
