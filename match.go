package jsondiff

import "errors"

// errCompareAborted is the sentinel panic used by the
// internal test seam to inject a recoverable failure in
// the middle of a comparison. It belongs to the same error
// layering as invalidJSONTypeError: CompareWithoutMarshal
// converts it into a regular returned error.
var errCompareAborted = errors.New("jsondiff: comparison aborted by internal test hook")

// matchStats contains diagnostic counters describing the
// work performed by the array matching and move/copy
// candidate accounting of a single Compare invocation.
//
// The counters are diagnostic only: they never influence
// the generated patch. Every invocation starts from a
// zero value, and each Differ owns its own counters, so
// concurrent comparisons never share state.
type matchStats struct {
	// arrays is the number of array comparisons that
	// requested a unified matcher.
	arrays int
	// hashes is the number of element digests computed
	// while hashing source/target array elements.
	hashes int
	// eqProbes is the number of old/new element pairs for
	// which semantic equality (deepEqual) was actually
	// evaluated, i.e. pairs that passed the digest filter.
	// Backtracking reuses the memo and adds memoHits only.
	eqProbes int
	// hashFiltered is the number of pairs rejected by the
	// digest negative filter without a deepEqual call.
	hashFiltered int
	// hashMatches is the number of pairs whose digests
	// are equal.
	hashMatches int
	// hashCollisions is the number of pairs whose
	// digests are equal but whose values are not, i.e.
	// genuine hash collisions that must never be
	// treated as matches.
	hashCollisions int
	// memoHits is the number of equality evaluations
	// served by the pair memo instead of recomputing
	// deepEqual.
	memoHits int
	// candidates is the number of distinct removed
	// elements probed while resolving a single add
	// into a move/copy/add operation.
	candidates int
	// copyProbes is the number of "unchanged" locations
	// probed while looking for a copy source.
	copyProbes int
}

// totalComparisons returns the overall number of old/new
// semantic comparisons performed, excluding memo hits and
// digest computations. It is the figure used by callers to
// quantify the comparison budget of a diff.
func (s *matchStats) totalComparisons() int {
	return s.eqProbes + s.candidates + s.copyProbes
}

// matcherHooks is an internal, package-only seam used by
// the regression tests to inject deterministic conditions
// (digest collisions and recoverable failures). It is not
// exported and the production path never sets it, so every
// hook site is a plain nil check in front of the exact
// production logic; there is no test-only algorithm.
type matcherHooks interface {
	// digest overrides the digest of a value. Returning
	// (h, true) uses h as the digest; otherwise the
	// regular hasher is used.
	digest(val interface{}) (uint64, bool)
	// onMatcher is invoked once a unified array matcher has
	// aligned the LCS pairs, before any operation is emitted
	// for the array.
	onMatcher(ptr pointer, m *arrayMatcher)
}

// arrayMatcher unifies, for a single pair of arrays, the
// four responsibilities that used to be scattered across
// compareArrays/compareArraysLCS/lcs/unorderedDeepEqualSlice:
//
//   - element identity hashing (digests),
//   - semantic equality, memoized per index pair,
//   - longest common subsequence alignment,
//   - move/copy candidate probing.
//
// Hashes are only a fast negative filter: equality is
// always confirmed with deepEqual, therefore digest
// collisions cannot pair distinct elements.
type arrayMatcher struct {
	src, tgt  []interface{}
	unordered bool

	sh []uint64
	th []uint64

	d *Differ

	// memo caches equality results for src index i vs
	// tgt index j, keyed by i<<32|j. Values: 1 equal,
	// 2 not equal.
	memo map[uint64]uint8
}

// newArrayMatcher builds a unified matcher and precomputes
// the digest of every element of both arrays exactly once.
func (d *Differ) newArrayMatcher(src, tgt []interface{}, unordered bool) *arrayMatcher {
	m := &arrayMatcher{
		src:       src,
		tgt:       tgt,
		unordered: unordered,
		sh:        make([]uint64, len(src)),
		th:        make([]uint64, len(tgt)),
		d:         d,
		memo:      make(map[uint64]uint8, len(src)*len(tgt)),
	}
	for i, v := range src {
		m.sh[i] = m.hashValue(v)
	}
	for i, v := range tgt {
		m.th[i] = m.hashValue(v)
	}
	d.stats.arrays++
	return m
}

func (m *arrayMatcher) hashValue(v interface{}) uint64 {
	m.d.stats.hashes++
	if m.d.hooks != nil {
		if h, ok := m.d.hooks.digest(v); ok {
			return h
		}
	}
	return m.d.hasher.digest(v, m.unordered)
}

// equal reports whether src[i] and tgt[j] are semantically
// equal. Digests are used as a negative filter, and every
// pair of indices is evaluated at most once; subsequent
// requests are answered from the memo.
func (m *arrayMatcher) equal(i, j int) bool {
	key := uint64(uint32(i))<<32 | uint64(uint32(j))
	if r, ok := m.memo[key]; ok {
		m.d.stats.memoHits++
		return r == 1
	}
	var result bool
	if m.sh[i] == m.th[j] {
		m.d.stats.hashMatches++
		// Hashes match: confirm with real equality. A
		// digest collision must never be treated as a match.
		// In unordered mode nested arrays are compared as
		// multisets as well.
		m.d.stats.eqProbes++
		if m.unordered {
			result = deepEqualUnordered(m.src[i], m.tgt[j])
		} else {
			result = deepEqual(m.src[i], m.tgt[j])
		}
		if !result {
			m.d.stats.hashCollisions++
		}
	} else {
		// Distinct digests: unequal without a deepEqual call.
		m.d.stats.hashFiltered++
		result = false
	}
	if result {
		m.memo[key] = 1
	} else {
		m.memo[key] = 2
	}
	return result
}

// lcsPairs returns the index pairs of the longest common
// subsequence, using the memoized equality of the matcher.
func (m *arrayMatcher) lcsPairs() [][2]int {
	n, p := len(m.src), len(m.tgt)
	t := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, p+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= p; j++ {
			if m.equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := n, p
	s := make([][2]int, 0, t[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.equal(i-1, j-1):
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

// unorderedEqual reports whether the two arrays contain the
// same multiset of elements regardless of their order.
// It pairs elements using digests as a filter and confirms
// every pairing with memoized equality, so duplicates are
// counted with multiplicity and digest collisions cannot
// produce a false positive.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	used := make([]bool, len(m.tgt))
	for i := range m.src {
		matched := -1
		for j := range m.tgt {
			if used[j] {
				continue
			}
			if m.equal(i, j) {
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

// deepEqualUnordered compares two JSON values the way
// deepEqual does, except that arrays are compared as
// multisets (order and duplicate positions are irrelevant),
// matching the semantics implied by unordered digests.
func deepEqualUnordered(src, tgt interface{}) bool {
	st := jsonTypeSwitch(src)
	if st != jsonTypeSwitch(tgt) {
		return false
	}
	switch st {
	case jsonArray:
		a := src.([]interface{})
		b := tgt.([]interface{})
		if len(a) != len(b) {
			return false
		}
		used := make([]bool, len(b))
		for _, x := range a {
			matched := false
			for j, y := range b {
				if !used[j] && deepEqualUnordered(x, y) {
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
	case jsonObject:
		o1 := src.(map[string]interface{})
		o2 := tgt.(map[string]interface{})
		if len(o1) != len(o2) {
			return false
		}
		for k, v := range o1 {
			v2, ok := o2[k]
			if !ok || !deepEqualUnordered(v, v2) {
				return false
			}
		}
		return true
	default:
		return deepEqual(src, tgt)
	}
}

// removedCandidate is a remove operation that may later be
// factorized into a move when a matching add is emitted.
// Keeping candidates in one ordered ledger guarantees that
// each removed element is probed at most once per add and
// consumed at most once overall, independently of nesting.
type removedCandidate struct {
	path  string
	val   interface{}
	hash  uint64
	opIdx int
	// When the remove belongs to a unified array matcher,
	// matcher/srcIdx allow candidate equality to reuse the
	// per-array memo instead of recomputing deepEqual.
	matcher *arrayMatcher
	srcIdx  int
}

// equalTo reports whether the candidate value is equal to
// v. The digest is used as a negative filter, and equality
// is always confirmed with deepEqual (or the matcher memo),
// so digest collisions cannot pair distinct elements.
func (c *removedCandidate) equalTo(d *Differ, vh uint64, v interface{}, matcher *arrayMatcher, tgtIdx int) bool {
	if c.hash != vh {
		// Distinct hashes: equality is not evaluated.
		return false
	}
	d.stats.candidates++
	if matcher != nil && c.matcher == matcher {
		return matcher.equal(c.srcIdx, tgtIdx)
	}
	return deepEqual(c.val, v)
}

// digestHash returns the digest of v using the Differ's
// configured hasher (and hook), without double counting
// element hashes performed by a matcher.
func (d *Differ) digestHash(v interface{}) uint64 {
	if d.hooks != nil {
		if h, ok := d.hooks.digest(v); ok {
			return h
		}
	}
	return d.hasher.digest(v, false)
}

// candidateHash returns the digest of a removed value,
// reusing the matcher's precomputed element digest when
// available so each element is hashed exactly once.
func (d *Differ) candidateHash(v interface{}, matcher *arrayMatcher, srcIdx int) uint64 {
	if matcher != nil && srcIdx >= 0 && srcIdx < len(matcher.sh) {
		return matcher.sh[srcIdx]
	}
	return d.digestHash(v)
}

// trackRemove registers a remove operation as a move
// candidate when factorization is enabled.
func (d *Differ) trackRemove(path string, v interface{}, matcher *arrayMatcher, srcIdx int) {
	if !d.opts.factorize {
		return
	}
	// The remove operation was just appended (possibly
	// preceded by a test operation when invertible), so its
	// index is the current patch length minus one.
	d.removes = append(d.removes, removedCandidate{
		path: path,
		val:  v,
		// Reuse the digest already computed by the matcher
		// that owns this element, counting it once; fallback
		// candidates (e.g. object removes) hash directly.
		hash:    d.candidateHash(v, matcher, srcIdx),
		opIdx:   len(d.patch) - 1,
		matcher: matcher,
		srcIdx:  srcIdx,
	})
}

// takeRemoved scans the ordered ledger for the first
// candidate equal to v, exactly mirroring the historical
// findRemoved semantics (earliest remove wins). It returns
// the candidate index in the ledger, or:
//   - -2 when an equal candidate cannot be moved into path
//     (proper prefix rule), preserving the historical
//     "do not fall back to an add" behavior,
//   - -1 when no candidate matches.
func (d *Differ) takeRemoved(vh uint64, v interface{}, path string, matcher *arrayMatcher, tgtIdx int) int {
	for i := range d.removes {
		c := &d.removes[i]
		if !c.equalTo(d, vh, v, matcher, tgtIdx) {
			continue
		}
		if hasProperPathPrefix(c.path, path) {
			return -2
		}
		return i
	}
	return -1
}

// consumeRemoved removes the ledger entry at idx, deletes
// its patch operation and shifts the remaining entries so
// their opIdx stay consistent with future patch mutations.
func (d *Differ) consumeRemoved(idx int) removedCandidate {
	c := d.removes[idx]
	d.patch = d.patch.remove(c.opIdx)
	d.removes = append(d.removes[:idx], d.removes[idx+1:]...)
	for i := range d.removes {
		if d.removes[i].opIdx > c.opIdx {
			d.removes[i].opIdx--
		}
	}
	return c
}

// insertOp inserts an operation at pos and keeps the
// candidate ledger indices aligned with the patch.
func (d *Differ) insertOp(pos int, typ, from, path string, src, tgt interface{}, vl int) {
	d.patch = d.patch.insert(pos, typ, from, path, src, tgt, vl)
	for i := range d.removes {
		if d.removes[i].opIdx >= pos {
			d.removes[i].opIdx++
		}
	}
}

// discardCandidatesFrom drops every candidate whose patch
// operation has been truncated away during rationalization.
func (d *Differ) discardCandidatesFrom(patchLen int) {
	kept := d.removes[:0]
	for _, c := range d.removes {
		if c.opIdx < patchLen {
			kept = append(kept, c)
		}
	}
	d.removes = kept
}

// findUnchangedCopy looks for an unchanged location whose
// value equals v and can serve as a copy source. The digest
// is only a negative filter: the candidate is confirmed with
// deepEqual, so a digest collision between distinct values
// cannot produce a copy to the wrong location.
func (d *Differ) findUnchangedCopy(v interface{}) string {
	if d.hashmap == nil {
		return emptyPointer
	}
	node, ok := d.hashmap[d.digestHash(v)]
	if !ok {
		return emptyPointer
	}
	d.stats.copyProbes++
	if deepEqual(node.val, v) {
		return node.ptr
	}
	// Genuine collision: the value stored under this digest
	// is different from v, so it is not a valid copy source.
	return emptyPointer
}
