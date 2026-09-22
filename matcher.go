package jsondiff

import "errors"

// Array matching is split in three responsibilities that all
// live in a single, short-lived arrayMatcher instance:
//
//   - digesting: each source/target element is hashed at most
//     once. Hashes are only ever used as a prefilter; two values
//     are considered equal solely on the result of deepEqual.
//   - equality: the result of comparing a source element with a
//     target element is memoized, so a given candidate pair is
//     resolved once. The LCS table, move/copy discovery and the
//     unordered equivalence check share those results.
//   - discovery: the LCS index pairs and the move candidate queue
//     are computed together, before any operation is emitted, so
//     a failure never leaves a half-edited patch behind.
//
// The counters held by the matcher are diagnostic data. They are
// copied onto the owning Differ only once the matching has fully
// succeeded; a matcher never mutates any shared state before
// that point, which makes a failed attempt (and its retry) free
// of any leaked cache, counter or temporary state.

// matcherHookFunc is invoked by the array matcher at well-defined
// phases. It is unexported and used exclusively by the package's
// own tests to inject recoverable or unsupported failures; the
// production functional options never set it.
type matcherHookFunc func(phase string) error

// Matcher failure classifications. A hook wraps one of these
// sentinel errors so that the Differ can distinguish a transient
// failure that can be retried from an input the matcher must
// explicitly report as unsupported instead of guessing.
var (
	errMatcherRecoverable = errors.New("jsondiff: array matcher reported a recoverable failure")
	errMatcherUnsupported = errors.New("jsondiff: array matcher does not support this input")
)

const (
	matcherPhaseLCS        = "array:lcs"
	matcherPhaseEquivalent = "array:equivalent"
)

// matcherStats holds diagnostic counters produced while matching
// arrays. The counters are informational only and never influence
// the generated patch.
type matcherStats struct {
	// elementComparisons is the number of times the matcher had
	// to resolve the equality of a source element with a target
	// element using deepEqual. Each candidate pair is counted at
	// most once.
	elementComparisons int
	// hashComparisons is the number of pairs that were resolved
	// solely by their hash, without invoking deepEqual.
	hashComparisons int
	// digestComputed is the number of element hashes computed.
	digestComputed int
	// multisetChecks reports how many times the matcher evaluated
	// the unordered (multiset) equivalence of two arrays.
	multisetChecks int
	// lcsChecks reports how many times the matcher computed the
	// longest common subsequence of two arrays.
	lcsChecks int
	// matchAttempts reports how many matcher executions
	// contributed to these counters. Only a successful execution
	// is ever committed, so a retry leaves this value untouched.
	matchAttempts int
}

func (s matcherStats) add(o matcherStats) matcherStats {
	s.elementComparisons += o.elementComparisons
	s.hashComparisons += o.hashComparisons
	s.digestComputed += o.digestComputed
	s.multisetChecks += o.multisetChecks
	s.lcsChecks += o.lcsChecks
	s.matchAttempts += o.matchAttempts
	return s
}

// arrayMatcher centralizes the matching of a single pair of
// arrays. A new instance is created for every array encountered
// while diffing, which makes its memoization and counters
// inherently isolated: concurrent comparisons that use distinct
// Differ instances never share matcher state.
type arrayMatcher struct {
	d   *Differ
	src []interface{}
	tgt []interface{}

	// Element digests, computed lazily and cached. A zero entry
	// is a valid maphash result; the booleans track memoization.
	sh   []uint64
	th   []uint64
	shOk []bool
	thOk []bool

	// memo[i][j] caches the equality of src[i] and tgt[j]:
	// 0 = not resolved, 1 = equal, -1 = not equal.
	memo [][]int8

	stats matcherStats

	// Pending move candidates found while walking the LCS, keyed
	// by the positional hash of the moved value. Each entry is a
	// local removal waiting to be paired with a later addition.
	localMoves map[uint64][]*pendingMove
}

// pendingMove is a removal emitted by the LCS walk that may be
// rewritten as a move operation when a matching addition follows.
type pendingMove struct {
	// srcIdx is the index of the removed element in the source
	// array.
	srcIdx int
	// removeOrdinal is the ordinal of the removal among local
	// remove operations still present in the patch (0-based). It
	// lets the walk locate the live remove operation without
	// keeping a pointer into the mutated patch.
	removeOrdinal int
	// path, computed at emission time using the running
	// add/remove adjustment, is the "from" location of the move.
	path string
	// consumed reports whether the removal has already been
	// rewritten as a move.
	consumed bool
}

// matchPlan is the result of matching two arrays. It is produced
// before any patch operation is emitted.
type matchPlan struct {
	pairs [][2]int
	stats matcherStats
}

func newArrayMatcher(d *Differ, src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		d:          d,
		src:        src,
		tgt:        tgt,
		sh:         make([]uint64, len(src)),
		th:         make([]uint64, len(tgt)),
		shOk:       make([]bool, len(src)),
		thOk:       make([]bool, len(tgt)),
		memo:       make([][]int8, len(src)),
		localMoves: make(map[uint64][]*pendingMove),
	}
	for i := range m.memo {
		m.memo[i] = make([]int8, len(tgt))
	}
	return m
}

// srcDigest/tgtDigest return the positional hash of an element,
// computing it at most once for the lifetime of the matcher.
func (m *arrayMatcher) srcDigest(i int) uint64 {
	if !m.shOk[i] {
		m.sh[i] = m.d.digest(m.src[i], false)
		m.shOk[i] = true
		m.stats.digestComputed++
	}
	return m.sh[i]
}

func (m *arrayMatcher) tgtDigest(j int) uint64 {
	if !m.thOk[j] {
		m.th[j] = m.d.digest(m.tgt[j], false)
		m.thOk[j] = true
		m.stats.digestComputed++
	}
	return m.th[j]
}

// equalAt resolves the equality of src[i] and tgt[j] exactly
// once. Distinct hashes short-circuit the comparison; equal
// hashes are always confirmed with deepEqual so that a hash
// collision can never pair two distinct or duplicated values.
func (m *arrayMatcher) equalAt(i, j int) bool {
	switch m.memo[i][j] {
	case 1:
		return true
	case -1:
		return false
	}
	eq := false
	if m.srcDigest(i) == m.tgtDigest(j) {
		eq = deepEqual(m.src[i], m.tgt[j])
		m.stats.elementComparisons++
	} else {
		m.stats.hashComparisons++
	}
	if eq {
		m.memo[i][j] = 1
	} else {
		m.memo[i][j] = -1
	}
	return eq
}

// lcs computes the longest common subsequence using memoized
// element equality, and returns the ascending index pairs of the
// matched elements.
func (m *arrayMatcher) lcs() [][2]int {
	m.stats.lcsChecks++

	n, p := len(m.src), len(m.tgt)
	tab := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		tab[i] = make([]int, p+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= p; j++ {
			if m.equalAt(i-1, j-1) {
				tab[i][j] = tab[i-1][j-1] + 1
			} else {
				tab[i][j] = max(tab[i-1][j], tab[i][j-1])
			}
		}
	}
	i, j := n, p
	s := make([][2]int, 0, tab[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.equalAt(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case tab[i-1][j] > tab[i][j-1]:
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

// multisetEqual reports whether the two arrays contain the same
// elements regardless of their order, honoring multiplicities.
// Elements are grouped by their hash, and equality is confirmed
// with deepEqual inside each bucket: duplicate values are matched
// one-to-one, and hash collisions never produce a false positive.
func (m *arrayMatcher) multisetEqual() bool {
	m.stats.multisetChecks++

	if len(m.src) != len(m.tgt) {
		return false
	}
	if len(m.src) == 0 {
		return true
	}
	buckets := make(map[uint64][]int, len(m.src))
	for i := range m.src {
		k := m.d.digest(m.src[i], true)
		m.stats.digestComputed++
		buckets[k] = append(buckets[k], i)
	}
	used := make([]bool, len(m.src))
	for j := range m.tgt {
		k := m.d.digest(m.tgt[j], true)
		m.stats.digestComputed++
		candidates := buckets[k]
		matched := -1
		for _, i := range candidates {
			if used[i] {
				continue
			}
			if m.equalAt(i, j) {
				matched = i
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

// hook invokes the configured failure hook, if any, and records
// an attempt that may be committed to the diagnostic counters.
func (m *arrayMatcher) hook(phase string) error {
	m.stats.matchAttempts++
	if h := m.d.opts.matcherHook; h != nil {
		if err := h(phase); err != nil {
			return err
		}
	}
	return nil
}

// runLCS validates the phase and computes the LCS plan. It never
// mutates the owning Differ: on failure the caller discards the
// matcher, so its partial memoization and counters cannot leak.
func (m *arrayMatcher) runLCS() (matchPlan, error) {
	if err := m.hook(matcherPhaseLCS); err != nil {
		return matchPlan{}, err
	}
	return matchPlan{
		pairs: m.lcs(),
		stats: m.stats,
	}, nil
}

// runEquivalent validates the phase and computes the unordered
// multiset equivalence. The same no-shared-state guarantee as
// runLCS applies.
func (m *arrayMatcher) runEquivalent() (bool, matcherStats, error) {
	if err := m.hook(matcherPhaseEquivalent); err != nil {
		return false, matcherStats{}, err
	}
	return m.multisetEqual(), m.stats, nil
}

// registerLocalMove records a local removal as a pending move
// candidate. Entries are created in emission order, keyed by the
// positional hash of the removed value.
func (m *arrayMatcher) registerLocalMove(srcIdx, ordinal int, path string) {
	k := m.srcDigest(srcIdx)
	m.localMoves[k] = append(m.localMoves[k], &pendingMove{
		srcIdx:        srcIdx,
		removeOrdinal: ordinal,
		path:          path,
	})
}

// takeLocalMove pairs the target element at index j with the
// first compatible, not-yet-consumed local removal. Duplicate
// values are consumed one-to-one (FIFO) and hash collisions are
// resolved with the memoized deepEqual check.
func (m *arrayMatcher) takeLocalMove(j int) *pendingMove {
	k := m.tgtDigest(j)
	for _, pm := range m.localMoves[k] {
		if pm.consumed {
			continue
		}
		if !m.equalAt(pm.srcIdx, j) {
			continue
		}
		pm.consumed = true
		m.renumber(pm.removeOrdinal)
		return pm
	}
	return nil
}

// renumber keeps the live ordinals contiguous after a pending
// removal has been rewritten as a move and deleted from the
// patch.
func (m *arrayMatcher) renumber(consumed int) {
	for _, list := range m.localMoves {
		for _, pm := range list {
			if !pm.consumed && pm.removeOrdinal > consumed {
				pm.removeOrdinal--
			}
		}
	}
}

// planLCS runs the LCS matching through the unified matcher. A
// recoverable failure is retried once with the exact same
// inputs; an unsupported failure (or an exhausted recovery)
// deterministically falls back to the legacy positional LCS. The
// matcher is instantiated and discarded internally, so a failed
// attempt cannot leak state into the successful result. A nil
// matcher signals that the legacy walk must be used.
func (d *Differ) planLCS(src, tgt []interface{}) ([][2]int, *arrayMatcher) {
	m := newArrayMatcher(d, src, tgt)
	plan, err := m.runLCS()
	if err != nil {
		if errors.Is(err, errMatcherRecoverable) {
			// Retry once with a fresh matcher and identical inputs;
			// the failed matcher is dropped entirely, so none of its
			// memoized results or counters are observable.
			rm := newArrayMatcher(d, src, tgt)
			if retry, err2 := rm.runLCS(); err2 == nil {
				d.matchStats = d.matchStats.add(retry.stats)
				return retry.pairs, rm
			}
		}
		// Explicitly unsupported: fall back to the original LCS
		// semantics, which remain the source of truth in that case.
		return lcs(src, tgt), nil
	}
	d.matchStats = d.matchStats.add(plan.stats)
	return plan.pairs, m
}

// planEquivalent runs the unordered multiset equivalence through
// the unified matcher. It applies the same recovery/fallback
// policy as planLCS; the fallback deliberately preserves the
// legacy (pre-existing) semantics, even though the production
// matcher is collision-safe.
func (d *Differ) planEquivalent(src, tgt []interface{}) bool {
	m := newArrayMatcher(d, src, tgt)
	eq, stats, err := m.runEquivalent()
	if err != nil {
		if errors.Is(err, errMatcherRecoverable) {
			if eq2, stats2, err2 := newArrayMatcher(d, src, tgt).runEquivalent(); err2 == nil {
				d.matchStats = d.matchStats.add(stats2)
				return eq2
			}
		}
		// Explicitly unsupported: preserve the original behavior.
		return d.unorderedDeepEqualSlice(src, tgt)
	}
	d.matchStats = d.matchStats.add(stats)
	return eq
}
