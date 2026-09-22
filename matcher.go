package jsondiff

// matcherStats holds diagnostic counters that quantify the
// amount of comparison work performed during a single
// comparison. The counters are diagnostic-only: they never
// influence the generated patch, its structure or its order.
// They are reset before every public comparison and live on
// the Differ instance, which makes concurrent comparisons
// using distinct Differ instances naturally isolated.
type matcherStats struct {
	// digests counts hash digest computations performed
	// on compared values.
	digests uint64
	// hashRejections counts (src, tgt) candidate pairs
	// that were rejected by the hash prefilter, i.e. without
	// running a deep equality comparison.
	hashRejections uint64
	// equalityProbes counts deep equality comparisons
	// performed between candidate values.
	equalityProbes uint64
	// moveCandidates counts (removed element, added value)
	// pairs probed while searching for a move operation.
	moveCandidates uint64
	// copyCandidates counts (unchanged location, added
	// value) pairs probed while searching for a copy.
	copyCandidates uint64
}

// arrayMatchInfo is the diagnostic snapshot handed to the
// internal, test-only array matching hook. It describes the
// unified work done for a single array comparison.
type arrayMatchInfo struct {
	// srcLen and tgtLen are the lengths of the source and
	// target arrays being matched.
	srcLen int
	tgtLen int
	// pairs are the LCS index pairs computed exactly once
	// for the comparison.
	pairs [][2]int
	// stats is a snapshot of the diagnostic counters taken
	// immediately after the match.
	stats matcherStats
}

// arrayMatcher unifies the responsibilities previously split
// between the lcs function, the unorderedDeepEqualSlice method
// and the factorize hashmap helpers:
//
//   - hashing: each compared element is digested once;
//   - equality: a hash prefilter guards every deep equality
//     probe, so distinct values never reach deepEqual and
//     equal values are never split by a hash collision;
//   - longest common subsequence: the LCS is computed from a
//     single pass over the (src, tgt) matrix;
//   - multiset equality: unordered comparison uses hash
//     buckets that are always verified with deep equality.
//
// Each (src, tgt) candidate pair is accounted for at most
// once: either it is rejected by the hash prefilter or it is
// probed for deep equality.
type arrayMatcher struct {
	src     []interface{}
	tgt     []interface{}
	srcHash []uint64
	tgtHash []uint64
	stats   *matcherStats
	digest  func(interface{}) uint64
}

// newArrayMatcher builds a matcher for the two arrays,
// computing the digest of every element exactly once.
// When sorted is true, arrays are hashed in sorted form, as
// required by the unordered-equivalence option; this mirrors
// the historical behavior of hasher.digest(v, true).
func (d *Differ) newArrayMatcher(src, tgt []interface{}, sorted bool) *arrayMatcher {
	if d.matcherStats == nil {
		d.matcherStats = &matcherStats{}
	}
	digest := func(v interface{}) uint64 {
		return d.digest(v, sorted)
	}
	m := &arrayMatcher{
		src:     src,
		tgt:     tgt,
		srcHash: make([]uint64, len(src)),
		tgtHash: make([]uint64, len(tgt)),
		stats:   d.matcherStats,
		digest:  digest,
	}
	for i, v := range src {
		m.srcHash[i] = digest(v)
	}
	for i, v := range tgt {
		m.tgtHash[i] = digest(v)
	}
	return m
}

// equal reports whether the source element at index i is
// deeply equal to the target element at index j. The hash
// prefilter runs first; on a hash match, deep equality is
// always verified so that a digest collision can never pair
// two distinct elements. Every pair is accounted exactly
// once, whether rejected by hash or probed for equality.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.srcHash[i] != m.tgtHash[j] {
		m.stats.hashRejections++
		return false
	}
	m.stats.equalityProbes++
	return deepEqual(m.src[i], m.tgt[j])
}

// lcs computes the longest common subsequence of the source
// and target arrays and returns the source/target index pairs
// of the common elements, in ascending order.
//
// The classic LCS dynamic programming matrix is traversed
// exactly once. Cells reached through an equality transition
// additionally record the flag in a separate matrix, so
// backtracking never performs additional equality
// comparisons. Every candidate pair is therefore evaluated
// at most once.
func (m *arrayMatcher) lcs() [][2]int {
	n, p := len(m.src), len(m.tgt)

	t := make([][]int, n+1)
	eq := make([][]bool, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, p+1)
		eq[i] = make([]bool, p+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= p; j++ {
			if m.equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
				eq[i][j] = true
			} else {
				v := t[i-1][j]
				if t[i][j-1] > v {
					v = t[i][j-1]
				}
				t[i][j] = v
			}
		}
	}

	i, j := n, p
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case eq[i][j]:
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
// same multiset of elements, ignoring ordering. Source
// elements are grouped by digest hash, then each target
// element is matched against its bucket using deep equality.
// Hash collisions are resolved correctly because membership
// is always verified, and duplicate values are matched by
// consuming one bucket entry per matched element.
func (m *arrayMatcher) unorderedEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	buckets := make(map[uint64][]int, len(m.src))
	for i := range m.src {
		k := m.srcHash[i]
		buckets[k] = append(buckets[k], i)
	}
	for j := range m.tgt {
		k := m.tgtHash[j]
		bucket, ok := buckets[k]
		if !ok {
			m.stats.hashRejections++
			return false
		}
		matched := -1
		for _, i := range bucket {
			m.stats.equalityProbes++
			if lenientDeepEqual(m.src[i], m.tgt[j]) {
				matched = i
				break
			}
		}
		if matched == -1 {
			return false
		}
		// Consume the matched source entry so that
		// duplicated values cannot be matched twice.
		// The remaining entries are rebuilt without reusing
		// the bucket backing array, which would otherwise be
		// overwritten during the iteration.
		remaining := make([]int, 0, len(bucket)-1)
		for _, i := range bucket {
			if i != matched {
				remaining = append(remaining, i)
			}
		}
		if len(remaining) == 0 {
			delete(buckets, k)
		} else {
			buckets[k] = remaining
		}
	}
	return true
}

// lenientDeepEqual behaves like deepEqual for the types
// produced by encoding/json. It additionally accepts plain
// Go integer values, which the historical hash-based
// unordered comparison tolerated; values of any other
// unsupported type, or values with different dynamic types,
// are reported as non-matching rather than guessed equal.
func lenientDeepEqual(src, tgt interface{}) bool {
	if si, ok := src.(int); ok {
		ti, ok := tgt.(int)
		return ok && si == ti
	}
	if jsonTypeSwitch(src) == jsonInvalid || jsonTypeSwitch(tgt) == jsonInvalid {
		return false
	}
	return deepEqual(src, tgt)
}
