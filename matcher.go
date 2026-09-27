package jsondiff

// This file unifies the responsibilities involved when comparing
// two JSON arrays:
//
//   - equality between elements (hash prefilter + authoritative
//     deep-equal verification, memoized so that any pair of values
//     is evaluated at most once),
//   - longest common subsequence discovery,
//   - unordered/multiset equality used by the Equivalent option,
//   - move/copy candidate discovery (the candidate ledger) used by
//     the Factorize option.
//
// The production comparison path (Differ.diff) is the only consumer;
// there is no alternate, test-only implementation of the algorithm.

// matcherStats holds diagnostic counters that quantify the work
// performed while matching arrays. Counters are diagnostics only:
// they never influence the generated patch. They are allocated on
// a per-comparison basis, which guarantees isolation between
// concurrent comparisons.
type matcherStats struct {
	// equalProbes is the number of authoritative deep-equal
	// evaluations performed while deciding whether two JSON
	// values are equal. Pairs that are rejected solely because
	// their hashes differ do not count.
	equalProbes int
	// hashDigests is the number of value digests computed.
	hashDigests int
	// hashRejects is the number of comparisons avoided (and
	// answered "not equal") because the hashes differed.
	hashRejects int
	// hashCollisions is the number of times different values
	// shared a digest, which forced a deep-equal verification.
	hashCollisions int
	// candidateProbes is the number of deep-equal evaluations
	// performed while discovering move/copy candidates.
	candidateProbes int
	// moveCandidates and copyCandidates count the move and
	// copy operations actually emitted through the ledger.
	moveCandidates int
	copyCandidates int
}

// eqKey identifies a pair of element positions within one
// arrayMatcher instance.
type eqKey struct{ i, j int }

// arrayMatcher matches the elements of two arrays using a single
// equality oracle. Every comparison between src[i] and tgt[j] is
// evaluated at most once: a differing digest answers "not equal"
// without a deep comparison, while equal digests are always
// verified with deepEqual, so digest collisions can never pair
// distinct elements incorrectly.
type arrayMatcher struct {
	d      *Differ
	src    []interface{}
	tgt    []interface{}
	sorted bool

	sh []uint64 // digests of src elements
	th []uint64 // digests of tgt elements

	cache map[eqKey]bool
}

// newArrayMatcher returns a matcher for the given arrays. When
// sorted is true, arrays are treated as unordered multisets and
// the digest pass uses the sorted/equivalent mode of the hasher;
// otherwise the positional (LCS) mode is used.
func (d *Differ) newArrayMatcher(src, tgt []interface{}, sorted bool) *arrayMatcher {
	m := &arrayMatcher{
		d:      d,
		src:    src,
		tgt:    tgt,
		sorted: sorted,
		cache:  make(map[eqKey]bool),
	}
	m.sh = make([]uint64, len(src))
	m.th = make([]uint64, len(tgt))
	for i, v := range src {
		m.sh[i] = d.digestValue(v, sorted)
	}
	for j, v := range tgt {
		m.th[j] = d.digestValue(v, sorted)
	}
	return m
}

// equalAt reports whether src[i] and tgt[j] are deeply equal.
// It is the sole equality predicate used by LCS and multiset
// matching, and each pair is evaluated only once.
func (m *arrayMatcher) equalAt(i, j int) bool {
	k := eqKey{i, j}
	if v, ok := m.cache[k]; ok {
		return v
	}
	var equal bool

	// A differing digest is a sound negative filter: the
	// hasher is deterministic within one comparison, and its
	// seed is constant for the whole digest pass.
	if m.sh[i] != m.th[j] {
		m.d.stats.hashRejects++
		equal = false
	} else {
		// Same digest: verify authoritatively. Distinct values
		// that collide are reported and never paired.
		m.d.stats.equalProbes++
		equal = deepEqual(m.src[i], m.tgt[j])
		if !equal {
			m.d.stats.hashCollisions++
		}
	}
	m.cache[k] = equal
	return equal
}

// lcsPairs computes the index pairs of the longest common
// subsequence using the memoized equality oracle. The LCS table
// and backtracking therefore never compare the same pair twice,
// and the tie-breaking rules are identical to the original
// dynamic program.
func (m *arrayMatcher) lcsPairs() [][2]int {
	sl, tl := len(m.src), len(m.tgt)
	tab := make([][]int, sl+1)
	for i := range tab {
		tab[i] = make([]int, tl+1)
	}
	for i := 1; i <= sl; i++ {
		for j := 1; j <= tl; j++ {
			if m.equalAt(i-1, j-1) {
				tab[i][j] = tab[i-1][j-1] + 1
			} else {
				tab[i][j] = max(tab[i-1][j], tab[i][j-1])
			}
		}
	}
	i, j := sl, tl
	pairs := make([][2]int, 0, tab[i][j])

	for i > 0 && j > 0 {
		switch {
		case m.equalAt(i-1, j-1):
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

// multisetEqual reports whether the two arrays contain the same
// elements regardless of their order, with duplicates honored.
// Equal digests are treated as buckets and every tentative match
// is verified with the authoritative equality predicate, which
// makes the result safe against digest collisions and repeated
// values.
func (m *arrayMatcher) multisetEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	used := make([]bool, len(m.tgt))

	for i := range m.src {
		matched := false
		for j := range m.tgt {
			if used[j] {
				continue
			}
			if m.equalAt(i, j) {
				used[j] = true
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

// removedCandidate describes an element removed from the source
// document that may later be reused as the source of a move.
type removedCandidate struct {
	id    int64
	path  string
	value interface{}
	hash  uint64
}

// candidateLedger is the document-wide ledger used by the
// Factorize option. It preserves the first-in-first-out order in
// which removals were emitted, which keeps move selection stable
// in the presence of duplicate values.
type candidateLedger struct {
	buckets map[uint64][]removedCandidate
}

func (l *candidateLedger) add(c removedCandidate) {
	if l.buckets == nil {
		l.buckets = make(map[uint64][]removedCandidate)
	}
	l.buckets[c.hash] = append(l.buckets[c.hash], c)
}

// unchangedNode records a value that is present at the same
// location in both documents, indexed by digest. Several nodes
// may share a digest when values repeat; slice order follows the
// deterministic (sorted-key) preparation traversal.
type unchangedNode struct {
	ptr string
	val interface{}
}
