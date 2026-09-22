package jsondiff

import "errors"

// errComparisonInterrupted is the sentinel used by the
// internal, diagnostic-only fault hook. It represents a
// recoverable interruption of a comparison and is
// converted into a regular error by public entry points.
var errComparisonInterrupted = errors.New("comparison interrupted by diagnostic fault hook")

// arrayMatcher centralizes every responsibility involved
// in matching two JSON arrays during a diff:
//
//   - content hashing (best-effort prefilter),
//   - equality resolution (the only authority),
//   - longest common subsequence (LCS) computation,
//   - move/copy candidate discovery.
//
// Hashes are never treated as a proof of equality: any
// hash match is confirmed with deep equality, so
// duplicate values and hash collisions cannot produce a
// wrong pairing. Equality between a given old/new element
// pair is resolved at most once for the whole matching
// phase, and the result is reused by both the LCS and
// move candidate discovery. This avoids maintaining
// parallel implementations of the comparison logic.
type arrayMatcher struct {
	src   []interface{}
	tgt   []interface{}
	d     *Differ
	stats *matchStats

	// srcHashes/tgtHashes hold element digests, computed
	// lazily at most once per element and only when an
	// unordered-equivalence prefilter is used.
	srcHashes []uint64
	tgtHashes []uint64

	// eqMemo maps a pair of element indices (src index in
	// the high 32 bits, tgt index in the low 32 bits) to
	// its resolved equality result.
	eqMemo map[uint64]bool

	// removed records, in emission order, every remove
	// operation generated for an element of the current
	// source array. It lets move discovery reuse the LCS
	// equality memo while preserving the historical "first
	// matching remove in patch order" rule.
	removed []removedElem

	// equalFn is the equality authority. Production paths
	// use deepEqual, whose panic on unrecognized value
	// types propagates through the public error model. The
	// standalone white-box helper installs a best-effort
	// predicate that additionally tolerates raw Go values;
	// the matching algorithm itself stays shared.
	equalFn func(a, b interface{}) bool
}

// newArrayMatcher builds a matcher for the two arrays.
// No hashing or equality evaluation is performed until
// the corresponding capability is actually required.
func (d *Differ) newArrayMatcher(src, tgt []interface{}) *arrayMatcher {
	if d.stats != nil {
		d.stats.matchers++
	}
	m := &arrayMatcher{
		src:     src,
		tgt:     tgt,
		d:       d,
		stats:   d.stats,
		equalFn: deepEqual,
	}
	return m
}

// removedElem records a remove operation emitted for
// src[idx]: the index of the operation in the patch, and
// whether it has already been consumed by a move.
type removedElem struct {
	srcIdx   int
	opIdx    int
	consumed bool
}

// noteRemoved records that the remove operation located
// at patch index opIdx was generated for src[srcIdx].
func (m *arrayMatcher) noteRemoved(srcIdx, opIdx int) {
	m.removed = append(m.removed, removedElem{
		srcIdx: srcIdx,
		opIdx:  opIdx,
	})
}

// takeRemoved finds the first still-available remove
// whose source element is deeply equal to tgt[tgtIdx],
// marks it consumed and returns its patch index together
// with the source index of the removed element. The
// boolean reports whether the remove belongs to the
// current array (false for removes emitted inside nested
// aligned pairs). It returns (-1, 0, false) when there is
// no candidate.
//
// Iteration order mirrors the historical linear scan of
// remove operations, and equality is resolved through the
// shared memo, so each old/new candidate is evaluated at
// most once.
func (m *arrayMatcher) takeRemoved(tgtIdx int) (opIdx, srcIdx int, current bool) {
	for k := range m.removed {
		r := &m.removed[k]
		if r.consumed {
			continue
		}
		if m.equal(r.srcIdx, tgtIdx) {
			r.consumed = true
			// Earlier consumed removes were physically
			// deleted from the patch; shift the index.
			shift := 0
			for _, e := range m.removed {
				if e.consumed && e.opIdx < r.opIdx {
					shift++
				}
			}
			return r.opIdx - shift, r.srcIdx, true
		}
	}
	return -1, 0, false
}

// liveFromIndex returns the current array index of the
// not-yet-moved source element at srcIdx. Earlier elements
// already consumed by moves no longer occupy a slot, so
// the value is srcIdx minus the number of consumed removes
// located before srcIdx. This matches the path the removed
// operation would carry at the time the add is emitted.
func (m *arrayMatcher) liveFromIndex(srcIdx int) int {
	offset := 0
	for _, r := range m.removed {
		if r.consumed && r.srcIdx < srcIdx {
			offset++
		}
	}
	return srcIdx - offset
}

// equal resolves whether src[i] and tgt[j] are deeply
// equal. The decision is computed once per pair and then
// memoized for both LCS and move candidate discovery.
func (m *arrayMatcher) equal(i, j int) bool {
	key := uint64(uint32(i))<<32 | uint64(uint32(j))
	if eq, ok := m.eqMemo[key]; ok {
		return eq
	}
	if m.stats != nil {
		m.stats.elementCompares++
	}
	eq := m.equalFn(m.src[i], m.tgt[j])
	if m.eqMemo == nil {
		m.eqMemo = make(map[uint64]bool)
	}
	m.eqMemo[key] = eq
	return eq
}

// lcsPairs computes the index pairs of the longest common
// subsequence using the shared equality memo. Equality is
// resolved exactly once for every pair instead of once per
// DP-cell fill and again during backtracking.
func (m *arrayMatcher) lcsPairs() [][2]int {
	src, tgt := m.src, m.tgt
	table := make([][]int, len(src)+1)
	for i := 0; i <= len(src); i++ {
		table[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i <= len(src); i++ {
		// Diagnostic hook: trigger an injected, recoverable
		// interruption halfway through the DP table fill.
		if hook := m.d.faultHook; hook != nil && i == (len(src)+1)/2 {
			hook()
		}
		for j := 1; j <= len(tgt); j++ {
			if m.equal(i-1, j-1) {
				table[i][j] = table[i-1][j-1] + 1
			} else {
				table[i][j] = max(table[i-1][j], table[i][j-1])
			}
		}
	}

	i, j := len(src), len(tgt)
	pairs := make([][2]int, 0, table[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.equal(i-1, j-1):
			pairs = append(pairs, [2]int{i - 1, j - 1})
			i--
			j--
		case table[i-1][j] > table[i][j-1]:
			i--
		default:
			j--
		}
	}
	for l, r := 0, len(pairs)-1; l < r; l, r = l+1, r-1 {
		pairs[l], pairs[r] = pairs[r], pairs[l]
	}
	return pairs
}

// srcHash/tgtHash return the digest hash of an element,
// computing it at most once.
func (m *arrayMatcher) srcHash(i int) uint64 {
	if m.srcHashes == nil {
		m.srcHashes = make([]uint64, len(m.src))
	}
	if h := m.srcHashes[i]; h != 0 {
		return h
	}
	h := m.d.digestElement(m.src[i], m.d.opts.equivalent)
	m.srcHashes[i] = h
	return h
}

func (m *arrayMatcher) tgtHash(j int) uint64 {
	if m.tgtHashes == nil {
		m.tgtHashes = make([]uint64, len(m.tgt))
	}
	if h := m.tgtHashes[j]; h != 0 {
		return h
	}
	h := m.d.digestElement(m.tgt[j], m.d.opts.equivalent)
	m.tgtHashes[j] = h
	return h
}

// unorderedEqual reports whether the two arrays contain
// the same elements regardless of order. Elements are
// grouped by hash and groups are confirmed by deep
// equality, so duplicate values are counted (multiset
// semantics) and equal hashes between distinct values can
// never cause a false match.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	if len(m.src) == 0 {
		return true
	}
	buckets := make(map[uint64][]int, len(m.src))
	for i := range m.src {
		h := m.srcHash(i)
		buckets[h] = append(buckets[h], i)
	}
	for j := range m.tgt {
		if m.stats != nil {
			m.stats.hashCompares++
		}
		h := m.tgtHash(j)
		candidates := buckets[h]
		matched := -1
		for _, i := range candidates {
			if m.equal(i, j) {
				matched = i
				break
			}
			// Same digest, distinct values: a genuine
			// collision. Equality remains the authority.
			if m.stats != nil {
				m.stats.hashCollisions++
			}
		}
		if matched == -1 {
			return false
		}
		for k, i := range candidates {
			if i == matched {
				buckets[h] = append(candidates[:k], candidates[k+1:]...)
				break
			}
		}
	}
	for _, remaining := range buckets {
		if len(remaining) != 0 {
			return false
		}
	}
	return true
}

// removedCandidate describes a resolved move source.
type removedCandidate struct {
	opIdx   int
	srcIdx  int
	current bool
}

// findRemovedPair resolves a move candidate: the remove
// operation whose value is deeply equal to the value being
// added, or the zero value (opIdx == -1) when there is
// none.
//
// Within an LCS array traversal (tgtIdx >= 0), removes
// generated for the current array are matched through the
// shared equality memo, so each old/new candidate pair is
// evaluated once. Other removes fall back to the
// historical, counted deep equality scan, preserving the
// first-match ordering.
func (d *Differ) findRemovedPair(m *arrayMatcher, tgtIdx int, v interface{}) removedCandidate {
	if d.stats != nil {
		d.stats.moveCandidates++
	}
	if m != nil && tgtIdx >= 0 {
		if opIdx, srcIdx, current := m.scanRemoved(tgtIdx); opIdx != -1 {
			return removedCandidate{opIdx: opIdx, srcIdx: srcIdx, current: current}
		}
		return removedCandidate{opIdx: -1}
	}
	return removedCandidate{opIdx: d.scanRemoved(v)}
}

// scanRemoved implements move discovery for the current
// LCS array traversal. Array-level removes use the
// ordered, memoized records; removes emitted outside the
// current array (e.g. inside nested aligned pairs) retain
// the historical counted deep equality scan.
func (m *arrayMatcher) scanRemoved(tgtIdx int) (opIdx, srcIdx int, current bool) {
	if opIdx, srcIdx, current := m.takeRemoved(tgtIdx); opIdx != -1 {
		return opIdx, srcIdx, current
	}
	v := m.tgt[tgtIdx]
	arrayLevel := make(map[int]struct{}, len(m.removed))
	for _, r := range m.removed {
		arrayLevel[r.opIdx] = struct{}{}
	}
	for i := 0; i < len(m.d.patch); i++ {
		op := m.d.patch[i]
		if op.Type != OperationRemove {
			continue
		}
		if _, ok := arrayLevel[i]; ok {
			continue
		}
		if m.stats != nil {
			m.stats.elementCompares++
		}
		if deepEqual(op.OldValue, v) {
			return i, -1, false
		}
	}
	return -1, 0, false
}

// scanRemoved scans remove operations for a value deeply
// equal to v, preserving the historical first-match
// ordering. Each comparison is counted.
func (d *Differ) scanRemoved(v interface{}) int {
	for i := 0; i < len(d.patch); i++ {
		op := d.patch[i]
		if op.Type != OperationRemove {
			continue
		}
		if d.stats != nil {
			d.stats.elementCompares++
		}
		if deepEqual(op.OldValue, v) {
			return i
		}
	}
	return -1
}

// findUnchanged locates a source location that remained
// unchanged and whose value is deeply equal to v, for a
// copy candidate. A hash hit is always confirmed by deep
// equality, preventing hash collisions from pairing
// distinct values.
func (d *Differ) findUnchanged(v interface{}) string {
	if d.stats != nil {
		d.stats.copyCandidates++
	}
	if d.hashmap == nil {
		return emptyPointer
	}
	k := d.digestValue(v)
	node, ok := d.hashmap[k]
	if !ok {
		return emptyPointer
	}
	if d.stats != nil {
		d.stats.hashCompares++
	}
	if !deepEqual(node.val, v) {
		if d.stats != nil {
			d.stats.hashCollisions++
		}
		return emptyPointer
	}
	return node.ptr
}
