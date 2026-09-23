package jsondiff

// MatchDiagnostics collects diagnostic counters about the array
// matching pipeline of a single comparison.
//
// Counters are diagnostic-only: they never influence the generated
// patch, and they are scoped to a single comparison. A successful
// comparison can retrieve them through
// [Differ.LastMatchDiagnostics].
type MatchDiagnostics struct {
	// ElementEqualChecks is the number of deepEqual evaluations
	// performed to pair old and new array elements, both while
	// computing the LCS and while resolving unordered/equivalent
	// arrays. Each (old, new) pair is evaluated at most once.
	ElementEqualChecks uint64

	// CandidateChecks is the number of deepEqual evaluations
	// performed while confirming move/copy candidates discovered
	// through digest hashing. Each candidate pair is confirmed
	// at most once.
	CandidateChecks uint64

	// HashDigests is the number of value digest computations
	// performed during array matching and move/copy discovery.
	// A given value is digested at most once per consuming stage.
	HashDigests uint64

	// HashCollisions is the number of times two distinct values
	// shared the same digest hash. A collision always falls back
	// to deepEqual, so digest collisions can never pair elements
	// incorrectly.
	HashCollisions uint64
}

// matchPhase identifies a stage of the unified array matching
// pipeline. It exists solely to locate the internal, test-only
// failure-injection hook.
type matchPhase uint8

const (
	matchFaultNone matchPhase = iota
	matchFaultPairing
	matchFaultCandidate
)

// matchFaultSpec configures the internal, test-only failure hook.
// The hook is unexported and cannot be reached from outside the
// package; production code never arms it.
type matchFaultSpec struct {
	phase matchPhase
	// after is the number of stage events observed before the
	// hook panics with a recoverable failure.
	after uint64
	// forceCollidingHashes makes every distinct value share the
	// same digest hash, exercising the deepEqual fallback. Equal
	// values still collide as usual.
	forceCollidingHashes bool
}

// matchFaultSentinel is wrapped into the package's existing
// invalidJSONTypeError so that an injected failure surfaces
// through the same error layer as invalid input.
type matchFaultSentinel struct{}

// forcedDigest is the non-zero digest returned when
// forceCollidingHashes is armed. It is deliberately constant so
// that collision tests are fully deterministic.
const forcedDigest uint64 = 0x6d61746368000001

// matchState carries everything that belongs to a single Compare
// invocation. It is allocated at the beginning of every
// comparison and discarded when it ends, so concurrent
// comparisons never share counters, hashes, candidate caches or
// failure hooks.
type matchState struct {
	hasher hasher
	// hashmap indexes values that are unchanged between source
	// and target, bucketed by digest. A digest is never trusted
	// on its own: every lookup is confirmed with deepEqual, which
	// also makes duplicate values and digest collisions safe.
	hashmap map[uint64][]jsonNode
	stats   matchStats
	fault   matchFaultSpec
	fired   uint64
}

type matchStats struct {
	equalChecks     uint64
	candidateChecks uint64
	digests         uint64
	collisions      uint64
}

func (st *matchState) diagnostics() MatchDiagnostics {
	return MatchDiagnostics{
		ElementEqualChecks: st.stats.equalChecks,
		CandidateChecks:    st.stats.candidateChecks,
		HashDigests:        st.stats.digests,
		HashCollisions:     st.stats.collisions,
	}
}

// digest returns the digest of val, counting the computation
// exactly once per call. Equal values always yield the same
// digest; the digest alone is never treated as equality.
func (st *matchState) digest(val interface{}, sort bool) uint64 {
	st.stats.digests++
	if st.fault.forceCollidingHashes {
		return forcedDigest
	}
	return st.hasher.digest(val, sort)
}

// fail fires the test-only failure hook for the given stage.
// When armed, it panics after the first `after` stage events.
func (st *matchState) fail(phase matchPhase) {
	if st.fault.phase != phase {
		return
	}
	st.fired++
	if st.fired > st.fault.after {
		panic(invalidJSONTypeError{t: matchFaultSentinel{}})
	}
}

// Equality memo states for an (oldIndex, newIndex) pair.
const (
	eqUnknown uint8 = iota
	eqTrue
	eqFalse
)

// arrayMatcher centralizes the responsibilities of matching two
// JSON arrays:
//
//   - digests provide a cheap prefilter and are computed once per
//     element position;
//   - equality is confirmed through deepEqual at most once per
//     (old, new) pair;
//   - confirmed pairs feed the LCS computation and the move/copy
//     candidate discovery, which share this single source of
//     truth instead of re-computing candidates.
//
// A nil state makes the matcher behave as a stateless equality
// oracle, which is used by the package-level lcs helper.
type arrayMatcher struct {
	st          *matchState
	src         []interface{}
	tgt         []interface{}
	eq          []uint8
	srcDigests  []uint64
	tgtDigests  []uint64
}

func newArrayMatcher(st *matchState, src, tgt []interface{}) *arrayMatcher {
	return &arrayMatcher{
		st:  st,
		src: src,
		tgt: tgt,
	}
}

func (m *arrayMatcher) ensureMemo() {
	if m.eq == nil {
		m.eq = make([]uint8, len(m.src)*len(m.tgt))
	}
}

// srcDigest returns the digest of src[i], computing it once.
func (m *arrayMatcher) srcDigest(i int) uint64 {
	if m.srcDigests == nil {
		m.srcDigests = make([]uint64, len(m.src))
	} else if h := m.srcDigests[i]; h != 0 {
		return h
	}
	h := m.st.digest(m.src[i], false)
	m.srcDigests[i] = h
	return h
}

// tgtDigest returns the digest of tgt[j], computing it once.
func (m *arrayMatcher) tgtDigest(j int) uint64 {
	if m.tgtDigests == nil {
		m.tgtDigests = make([]uint64, len(m.tgt))
	} else if h := m.tgtDigests[j]; h != 0 {
		return h
	}
	h := m.st.digest(m.tgt[j], false)
	m.tgtDigests[j] = h
	return h
}

// equal reports whether src[i] and tgt[j] are deeply equal. The
// result is memoized: deepEqual runs at most once for a given
// pair, even across the fill and backtracking passes of the LCS.
func (m *arrayMatcher) equal(i, j int) bool {
	m.ensureMemo()
	k := i*len(m.tgt) + j

	switch m.eq[k] {
	case eqTrue:
		return true
	case eqFalse:
		return false
	}

	if m.st != nil {
		m.st.stats.equalChecks++
		m.st.fail(matchFaultPairing)
	}
	v := eqFalse
	if deepEqual(m.src[i], m.tgt[j]) {
		v = eqTrue
	} else if m.st != nil && m.srcDigest(i) == m.tgtDigest(j) {
		// The hashes matched but the values are not equal:
		// record the collision that deepEqual just defeated.
		m.st.stats.collisions++
	}
	m.eq[k] = v
	return v == eqTrue
}

// canMatch reports whether a pair can be part of the LCS, using
// digests as a prefilter that never rejects equal values.
func (m *arrayMatcher) canMatch(i, j int) bool {
	m.ensureMemo()
	k := i*len(m.tgt) + j
	if m.eq[k] != eqUnknown {
		return m.eq[k] == eqTrue
	}
	if m.st != nil && m.srcDigest(i) != m.tgtDigest(j) {
		m.eq[k] = eqFalse
		return false
	}
	return m.equal(i, j)
}

// lcsPairs computes the index pairs of the longest common
// subsequence using the memoized equality oracle. The backtrack
// pass performs no additional equality evaluation.
func (m *arrayMatcher) lcsPairs() [][2]int {
	sl, tl := len(m.src), len(m.tgt)
	t := make([][]int, sl+1)
	for i := 0; i <= sl; i++ {
		t[i] = make([]int, tl+1)
	}

	for i := 1; i <= sl; i++ {
		for j := 1; j <= tl; j++ {
			if m.canMatch(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}

	i, j := sl, tl
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

// unorderedMultisetEqual reports whether src and tgt contain the
// same multiset of values, regardless of their order. Digests
// only narrow down the candidates; multiplicity is resolved with
// deepEqual, so duplicate values and digest collisions can never
// be paired incorrectly.
func (st *matchState) unorderedMultisetEqual(src, tgt []interface{}) bool {
	if len(src) != len(tgt) {
		return false
	}

	buckets := make(map[uint64][]int, len(tgt))
	used := make([]bool, len(tgt))
	for j, v := range tgt {
		h := st.digest(v, true)
		buckets[h] = append(buckets[h], j)
	}

	for _, v := range src {
		h := st.digest(v, true)
		matched := -1
		for _, j := range buckets[h] {
			if used[j] {
				continue
			}
			st.stats.equalChecks++
			if deepEqual(v, tgt[j]) {
				matched = j
				break
			}
			st.stats.collisions++
		}
		if matched == -1 {
			return false
		}
		used[matched] = true
	}
	return true
}

// withMatchFault arms the internal, test-only failure hook at the
// given pipeline stage, after `after` stage events have been
// observed. The failure traverses the public Compare entry points
// and is reported through the package's existing error layer.
func withMatchFault(phase matchPhase, after uint64) Option {
	return func(d *Differ) {
		d.fault.phase = phase
		d.fault.after = after
	}
}

// withForcedCollidingHashes makes every distinct value share the
// same digest hash. The production algorithm still has to resolve
// everything through deepEqual, which is what the collision
// behavior guards assert.
func withForcedCollidingHashes() Option {
	return func(d *Differ) {
		d.fault.forceCollidingHashes = true
	}
}
