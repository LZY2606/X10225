package jsondiff

// matchDigestFn is the digest function used by the array
// matching machinery. It is shared with the rest of the
// production code (see hasher.digest); the indirection
// exists only so that the diagnostics machinery can count
// digestions deterministically.
type matchDigestFn func(val interface{}, sort bool) uint64

// arrayDiffStats contains diagnostic counters about a
// comparison. The counters are purely diagnostic: they
// never influence the generated patch, and each comparison
// resets and owns its counters, which guarantees isolation
// between concurrent comparisons that use distinct
// Differ instances.
type arrayDiffStats struct {
	// deepEqualCalls is the number of deep-equality
	// comparisons performed while matching arrays,
	// including LCS cell probes, unordered-equivalence
	// checks and move/copy candidate checks.
	deepEqualCalls int
	// digestCalls is the number of value digests
	// computed while matching arrays.
	digestCalls int
	// candidateChecks is the number of deep-equality
	// comparisons performed while resolving a move or
	// copy candidate against an added value.
	candidateChecks int
	// fallbackCount is the number of times the unified
	// array matcher refused an input and the comparison
	// fell back to the legacy array algorithm.
	fallbackCount int
}

// moveCandidate is a removed value that may be reused by
// a later addition, either as a move or a copy source.
// Candidates are discovered once and consumed at most
// once, in the stable order in which removals are emitted.
type moveCandidate struct {
	path string
	val  interface{}
	hash uint64
	used bool
}

// arrayMatcher owns the responsibilities that were
// previously spread across lcs, hashing probes and
// patch scanning:
//
//   - hashing elements, at most once per element;
//   - deep-equality, probed at most once per index pair
//     (and per candidate/value pair), with hashes acting
//     as a pre-filter and never as a proof of equality;
//   - the longest common subsequence computation;
//   - move/copy candidate discovery.
type arrayMatcher struct {
	src    []interface{}
	tgt    []interface{}
	digest matchDigestFn

	// sh, th hold the lazily-computed digest of every
	// source and target element, each computed once.
	sh    []uint64
	th    []uint64
	shSet []bool
	thSet []bool

	// eqCmp is a tri-state memoization table indexed by
	// source/target element indices: 0 unknown, 1 equal,
	// -1 not equal.
	eqCmp []int8

	stats *arrayDiffStats

	// failHook, when non-nil, is invoked before the LCS
	// table is built. It may return errMatchUnavailable to
	// request a recoverable retry. The production path never
	// installs an hook.
	failHook func(phase string) error
}

// errMatchUnavailable is the internal sentinel used to
// signal that the unified matcher cannot take charge of an
// input. It is not an error surfaced to callers: the differ
// recovers by falling back to the legacy array algorithm,
// which is the original semantics. Genuine invalid inputs
// keep using the existing error layering.
var errMatchUnavailable = matchError{"array match unavailable"}

type matchError struct{ msg string }

func (e matchError) Error() string { return "jsondiff: " + e.msg }

// newArrayMatcher builds a matcher for one array comparison.
func newArrayMatcher(src, tgt []interface{}, digest matchDigestFn, stats *arrayDiffStats) *arrayMatcher {
	n, m := len(src), len(tgt)
	return &arrayMatcher{
		src:    src,
		tgt:    tgt,
		digest: digest,
		sh:     make([]uint64, n),
		th:     make([]uint64, m),
		shSet:  make([]bool, n),
		thSet:  make([]bool, m),
		eqCmp:  make([]int8, n*m),
		stats:  stats,
	}
}

// srcHash returns the digest of source element i.
func (m *arrayMatcher) srcHash(i int) uint64 {
	if !m.shSet[i] {
		m.sh[i] = m.digest(m.src[i], false)
		m.shSet[i] = true
		m.stats.digestCalls++
	}
	return m.sh[i]
}

// tgtHash returns the digest of target element j.
func (m *arrayMatcher) tgtHash(j int) uint64 {
	if !m.thSet[j] {
		m.th[j] = m.digest(m.tgt[j], false)
		m.thSet[j] = true
		m.stats.digestCalls++
	}
	return m.th[j]
}

// equal reports whether source element i and target element
// j are deeply equal. Hashes are only a pre-filter: when
// they collide, deep equality resolves the ambiguity, so
// distinct elements that share a digest can never be paired
// erroneously. Each pair is probed at most once.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.srcHash(i) != m.tgtHash(j) {
		m.eqCmp[i*len(m.tgt)+j] = -1
		return false
	}
	if v := m.eqCmp[i*len(m.tgt)+j]; v != 0 {
		return v == 1
	}
	m.stats.deepEqualCalls++
	if deepEqual(m.src[i], m.tgt[j]) {
		m.eqCmp[i*len(m.tgt)+j] = 1
		return true
	}
	m.eqCmp[i*len(m.tgt)+j] = -1
	return false
}

// lcs computes the index pairs of the longest common
// subsequence. Each element pair is probed through equal,
// which means repeated equalities are evaluated once.
func (m *arrayMatcher) lcs() ([][2]int, error) {
	if m.failHook != nil {
		if err := m.failHook("lcs"); err != nil {
			return nil, err
		}
	}
	n, k := len(m.src), len(m.tgt)
	t := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, k+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= k; j++ {
			if m.equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := n, k
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
	for l, r := 0, len(s)-1; l < r; l, r = l+1, r-1 {
		s[l], s[r] = s[r], s[l]
	}
	return s, nil
}

// equalCandidates reports whether value v is deeply equal
// to candidate c. A digest mismatch settles the question
// without a deep comparison; colliding digests always fall
// back to deep equality. Each candidate/value pair is
// probed at most once.
func equalCandidates(c *moveCandidate, v interface{}, digest matchDigestFn, stats *arrayDiffStats) bool {
	if digest != nil {
		stats.digestCalls++
		if h := digest(v, false); h != c.hash {
			return false
		}
	}
	stats.deepEqualCalls++
	stats.candidateChecks++
	return deepEqual(c.val, v)
}
