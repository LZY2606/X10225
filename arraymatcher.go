package jsondiff

import "errors"

// errArrayMatchAborted is the sentinel used internally to
// signal that an array matching attempt could not be
// completed. It represents an explicit "unknown" outcome;
// callers must recover by re-running the same production
// algorithm with the naive equality oracle, never guessing
// a successful result. The sentinel is unexported because it
// is part of the internal error layering and never escapes
// the public API, which only returns [Patch] values.
var errArrayMatchAborted = errors.New("jsondiff: array matching aborted")

// matchStage identifies the stage at which a failure
// injection hook fired.
type matchStage uint8

const (
	stageUnknown matchStage = iota
	stageDigest
	stageLCS
	stageMultiset
)

func (s matchStage) String() string {
	switch s {
	case stageDigest:
		return "digest"
	case stageLCS:
		return "lcs"
	case stageMultiset:
		return "multiset"
	default:
		return "unknown"
	}
}

// ArrayMatchStats gathers diagnostic counters produced by a
// single array matching attempt. Counters are observational
// only: they never influence the generated patch, and each
// top-level Compare call starts from a zeroed set, so
// concurrent comparisons on distinct [Differ] instances are
// fully isolated.
type ArrayMatchStats struct {
	// Path is the JSON pointer of the matched array.
	Path string
	// Stage is the stage that produced the statistics.
	Stage matchStage
	// Recovered reports whether the attempt was aborted by
	// the failure-injection seam and recovered with the
	// naive equality oracle.
	Recovered bool
	// SrcLen and TgtLen are the lengths of the compared
	// arrays.
	SrcLen int
	TgtLen int
	// Digests is the number of value digests computed while
	// matching the array. Each distinct element position is
	// digested at most once.
	Digests int
	// HashProbes is the number of equality probes answered
	// without falling back to a deep comparison, i.e. the
	// hash already proved the values different.
	HashProbes int
	// DeepEqualCalls counts structural equality comparisons
	// performed by the matcher. This is the "old-vs-new
	// comparison" metric.
	DeepEqualCalls int
	// HashCollisions counts probes where equal digests did
	// not correspond to equal values. Such probes always
	// resolve through DeepEqual, so duplicate elements can
	// never be paired on a hash coincidence.
	HashCollisions int
}

// arrayMatcher centralizes the four responsibilities that
// used to be scattered across lcs, unorderedDeepEqualSlice,
// findRemoved and findUnchanged:
//
//   - hashing: each value is digested at most once;
//   - equality: hash buckets are a prefilter, deepEqual is
//     the sole source of truth, and results are memoized;
//   - longest common subsequence: a single generic core,
//     parameterized by the equality oracle;
//   - move/copy discovery: candidate accounting is performed
//     once by the Differ through the same digest/equality
//     primitives.
//
// An arrayMatcher is a short-lived, stack-scoped object, so
// no state survives a comparison regardless of its outcome.
type arrayMatcher struct {
	d        *Differ
	src      []interface{}
	tgt      []interface{}
	srcH     []uint64
	tgtH     []uint64
	stats    ArrayMatchStats
	failHook matchFailHook
	stage    matchStage

	// eqCache memoizes equality results for a given pair of
	// source/target indices. Values are 0 (unprobed), 1
	// (equal) and 2 (not equal).
	eqCache map[[2]int]uint8
}

// newArrayMatcher builds a matcher bound to d. The failHook
// field is inherited from the Differ; in production it is
// nil and the matcher can never fail.
func newArrayMatcher(d *Differ, path string, src, tgt []interface{}) *arrayMatcher {
	return &arrayMatcher{
		d:        d,
		src:      src,
		tgt:      tgt,
		srcH:     make([]uint64, len(src)),
		tgtH:     make([]uint64, len(tgt)),
		failHook: d.failHook,
		stats: ArrayMatchStats{
			Path:   path,
			SrcLen: len(src),
			TgtLen: len(tgt),
		},
	}
}

// digest computes the digest of v once for this matcher and
// accounts it. When sort is true, array elements are hashed
// without regard to their order, which is only used by the
// unordered multiset comparison.
func (m *arrayMatcher) digest(v interface{}, sort bool) (uint64, error) {
	m.stats.Digests++
	if m.failHook != nil && m.stage == stageDigest {
		if m.failHook(stageDigest, m.stats.Path) {
			return 0, errArrayMatchAborted
		}
	}
	return m.d.digest(v, sort), nil
}

// digestAll hashes every source and target element exactly
// once. A failure at this stage leaves the matcher without
// any usable state and must be recovered by the caller. The
// active stage is restored on return so that later phases
// (LCS, multiset) keep observing their own stage.
func (m *arrayMatcher) digestAll(sort bool) error {
	stage := m.stage
	m.stage = stageDigest
	defer func() { m.stage = stage }()
	for i, v := range m.src {
		h, err := m.digest(v, sort)
		if err != nil {
			return err
		}
		m.srcH[i] = h
	}
	for i, v := range m.tgt {
		h, err := m.digest(v, sort)
		if err != nil {
			return err
		}
		m.tgtH[i] = h
	}
	return nil
}

// equal answers whether src[i] equals tgt[j]. Hash
// inequality decides negatively without any structural
// comparison; hash equality is always confirmed with
// deepEqual, so hash collisions cannot produce a wrong
// pairing. Positive and negative results are memoized per
// index pair so the LCS table fill and the backtrack share
// a single comparison budget.
func (m *arrayMatcher) equal(i, j int) (bool, error) {
	key := [2]int{i, j}
	if v, ok := m.eqCache[key]; ok {
		return v == 1, nil
	}
	if m.srcH[i] != m.tgtH[j] {
		m.stats.HashProbes++
		if m.eqCache == nil {
			m.eqCache = make(map[[2]int]uint8)
		}
		m.eqCache[key] = 2
		return false, nil
	}
	m.stats.DeepEqualCalls++
	if m.failHook != nil && m.stage == stageLCS {
		if m.failHook(stageLCS, m.stats.Path) {
			return false, errArrayMatchAborted
		}
	}
	if !deepEqual(m.src[i], m.tgt[j]) {
		m.stats.HashCollisions++
		if m.eqCache == nil {
			m.eqCache = make(map[[2]int]uint8)
		}
		m.eqCache[key] = 2
		return false, nil
	}
	if m.eqCache == nil {
		m.eqCache = make(map[[2]int]uint8)
	}
	m.eqCache[key] = 1
	return true, nil
}

// align computes the LCS index pairs using the shared LCS
// core and the matcher's collision-safe equality oracle.
func (m *arrayMatcher) align() ([][2]int, error) {
	m.stage = stageLCS
	return lcsPairs(len(m.src), len(m.tgt), m.equal)
}

// multisetEqual reports whether the two arrays contain the
// same elements regardless of their order, treating
// duplicates with their multiplicity. Elements are grouped
// by digest: a missing digest decides negatively, while a
// present digest is always confirmed with deepEqual against
// a not-yet-consumed candidate. Collisions therefore cannot
// hide different values or alter multiplicities.
//
// Elements are hashed with the order-insensitive variant,
// which sorts array values in place; this preserves the
// historical side effect of the Equivalent option.
func (m *arrayMatcher) multisetEqual() (bool, error) {
	m.stage = stageMultiset
	if len(m.src) != len(m.tgt) {
		return false, nil
	}
	if err := m.digestAll(true); err != nil {
		return false, err
	}
	// Group source indices per digest, tracking how many
	// occurrences of each index are still available.
	type slot struct {
		idx  int
		used bool
	}
	buckets := make(map[uint64][]slot, len(m.src))
	for i, h := range m.srcH {
		buckets[h] = append(buckets[h], slot{idx: i})
	}
	consumed := make(map[int]struct{}, len(m.src))

	for j, h := range m.tgtH {
		candidates := buckets[h]
		matched := false
		for k := range candidates {
			if candidates[k].used {
				continue
			}
			i := candidates[k].idx
			m.stats.DeepEqualCalls++
			if m.failHook != nil && m.stage == stageMultiset {
				if m.failHook(stageMultiset, m.stats.Path) {
					return false, errArrayMatchAborted
				}
			}
			if deepEqual(m.src[i], m.tgt[j]) {
				candidates[k].used = true
				consumed[i] = struct{}{}
				matched = true
				break
			}
			m.stats.HashCollisions++
		}
		if !matched {
			m.stats.HashProbes++
			return false, nil
		}
	}
	return len(consumed) == len(m.src), nil
}

// lcsPairs is the unique LCS implementation used by both the
// optimized path and the naive recovery path; only the eq
// oracle differs. The table fill and the backtrack are
// exactly the classic algorithm, keeping pair selection
// deterministic and stable.
func lcsPairs(nsrc, ntgt int, eq func(i, j int) (bool, error)) ([][2]int, error) {
	t := make([][]int, nsrc+1)
	for i := 0; i <= nsrc; i++ {
		t[i] = make([]int, ntgt+1)
	}
	for i := 1; i <= nsrc; i++ {
		for j := 1; j <= ntgt; j++ {
			eq, err := eq(i-1, j-1)
			if err != nil {
				return nil, err
			}
			if eq {
				t[i][j] = t[i-1][j-1] + 1
			} else if t[i-1][j] > t[i][j-1] {
				t[i][j] = t[i-1][j]
			} else {
				t[i][j] = t[i][j-1]
			}
		}
	}
	i, j := nsrc, ntgt
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		eq, err := eq(i-1, j-1)
		if err != nil {
			return nil, err
		}
		switch {
		case eq:
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
	return s, nil
}

// matchFailHook is the internal, test-only seam used to
// inject a recoverable failure in the middle of an array
// matching attempt. It is invoked from the production code
// path only; there is no test-only copy of the algorithm.
// Returning true aborts the current attempt.
type matchFailHook func(stage matchStage, path string) bool
