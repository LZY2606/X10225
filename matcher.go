package jsondiff

import (
	"errors"
	"sync"
)

// ErrUnsupported is returned when a comparison cannot be completed
// because an internal stage refuses to process the given input, or
// when an explicitly injected failure has aborted the comparison.
// It is part of the library's error hierarchy: callers may use
// errors.Is to detect the condition and safely retry the exact same
// comparison.
var ErrUnsupported = errors.New("jsondiff: comparison aborted or unsupported input")

// ArrayMatchStats reports diagnostic counters collected while a
// single pair of JSON arrays was matched.
//
// All counters are diagnostic only. They never influence the generated
// patch, and they are isolated per comparison: a Differ reused
// concurrently (or across calls) never shares them.
type ArrayMatchStats struct {
	// Lengths of the source and target arrays that were matched.
	SourceLen int `json:"source_len"`
	TargetLen int `json:"target_len"`

	// Pairs is the number of index pairs of the longest common
	// subsequence, i.e. elements that are unchanged in order.
	Pairs int `json:"pairs"`

	// Comparisons counts the number of distinct source/target element
	// pairs whose deep equality had to be evaluated. With the unified
	// matcher each pair is evaluated at most once per array.
	Comparisons int `json:"comparisons"`

	// CacheHits counts equality queries that were served from the
	// memoized equality table instead of recomputed. It covers both
	// the LCS backtracking walk and the move/copy candidate discovery,
	// which share a single candidate table.
	CacheHits int `json:"cache_hits"`

	// Moves counts the number of removed elements that an addition
	// was paired with, producing a move operation.
	Moves int `json:"moves"`

	// CopyCandidates counts elements whose addition was served by a
	// copy of an unchanged value found through factorization.
	CopyCandidates int `json:"copy_candidates"`

	// HashDigests counts value digests computed for hashing stages
	// such as unordered-equivalence checks and unchanged-value lookup.
	HashDigests int `json:"hash_digests"`
}

// MatchStats aggregates the diagnostic counters of a single
// comparison. The zero value represents a comparison that did not
// compare any arrays.
type MatchStats struct {
	// Arrays holds the counters of every array matched during the
	// comparison, in the deterministic order in which arrays are
	// visited (document order, top-level first).
	Arrays []ArrayMatchStats `json:"arrays"`

	// Comparisons is the total number of element pairs whose deep
	// equality had to be evaluated across every stage, including
	// stages that do not use the memoized table.
	Comparisons int `json:"comparisons"`

	// HashDigests is the total number of value digests computed.
	HashDigests int `json:"hash_digests"`
}

func (s MatchStats) clone() MatchStats {
	if s.Arrays != nil {
		s.Arrays = append([]ArrayMatchStats(nil), s.Arrays...)
	}
	return s
}

// eqResult is the tri-state result of an equality query. The zero
// value indicates that the pair has not been evaluated yet.
type eqResult int8

const (
	eqUnknown eqResult = 0
	eqEqual   eqResult = 1
	eqUnequal eqResult = -1
)

// arrayMatcher owns the responsibilities shared while matching one
// pair of JSON arrays:
//
//   - deep equality between source/target elements (memoized, so a
//     candidate pair is evaluated exactly once);
//   - the longest common subsequence table built on top of equality;
//   - move/copy candidate discovery, which reuses the same table
//     instead of recomputing equality.
//
// A matcher is created for each array pair and is never shared
// between arrays or between comparisons.
type arrayMatcher struct {
	src []interface{}
	tgt []interface{}

	// table is a packed triangular memoization table indexed by
	// i*len(tgt)+j. Values use eqResult encoding.
	table []eqResult

	// LCS DP lengths table, built in one pass over the memo table.
	dp [][]int

	stats ArrayMatchStats
}

func newArrayMatcher(src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		src:   src,
		tgt:   tgt,
		table: make([]eqResult, len(src)*len(tgt)),
	}
	m.stats.SourceLen = len(src)
	m.stats.TargetLen = len(tgt)
	return m
}

// equal reports whether src[si] is deeply equal to tgt[ti]. The
// result of each distinct pair is evaluated once; later queries,
// issued either while building the LCS or while discovering
// move/copy candidates, are served from the memo table.
func (m *arrayMatcher) equal(si, ti int) bool {
	k := si*len(m.tgt) + ti
	switch m.table[k] {
	case eqEqual:
		m.stats.CacheHits++
		return true
	case eqUnequal:
		m.stats.CacheHits++
		return false
	default:
		m.stats.Comparisons++
		if deepEqual(m.src[si], m.tgt[ti]) {
			m.table[k] = eqEqual
			return true
		}
		m.table[k] = eqUnequal
		return false
	}
}

// lcs builds the DP table and returns the index pairs of the longest
// common subsequence, in ascending order on both axes. Equality is
// routed through the memoized equal method, so candidate evaluation
// happens only during this single DP fill.
func (m *arrayMatcher) lcs() [][2]int {
	n, q := len(m.src), len(m.tgt)

	t := make([][]int, n+1)
	for i := range t {
		t[i] = make([]int, q+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= q; j++ {
			if m.equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else if t[i-1][j] >= t[i][j-1] {
				t[i][j] = t[i-1][j]
			} else {
				t[i][j] = t[i][j-1]
			}
		}
	}
	m.dp = t

	i, j := n, q
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
	for x, y := 0, len(s)-1; x < y; x, y = x+1, y-1 {
		s[x], s[y] = s[y], s[x]
	}
	m.stats.Pairs = len(s)
	return s
}

// failAfter aborts the comparison when the matchFailpoint threshold
// is reached, simulating a recoverable failure in the middle of
// array matching. Production comparisons never install a failpoint,
// so the check is a single nil comparison on the hot path.
func (m *arrayMatcher) failAfter(d *Differ) {
	if d.matchFailpoint == nil {
		return
	}
	d.matchFailpoint.mu.Lock()
	seen := d.matchFailpoint.seen
	d.matchFailpoint.mu.Unlock()
	if seen < d.matchFailpoint.after {
		d.matchFailpoint.mu.Lock()
		d.matchFailpoint.seen++
		d.matchFailpoint.mu.Unlock()
		return
	}
	d.abort(ErrUnsupported)
}

// matchFailpoint is a test-only seam used to inject a recoverable
// failure at a deterministic point of an array comparison. It is not
// reachable through any public option.
type matchFailpoint struct {
	mu    sync.Mutex
	after int
	seen  int
}
