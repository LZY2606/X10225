package jsondiff

import "fmt"

// matchStats gathers diagnostic counters about the array
// matching work performed during a comparison. The counters
// are strictly diagnostic: they are never consulted by the
// diff generation, and each Differ instance owns its own
// set, so concurrent comparisons are fully isolated.
type matchStats struct {
	// hashDigests is the number of values that were hashed
	// for array matching or move/copy candidate discovery.
	hashDigests int
	// equalChecks is the number of deep-equal comparisons
	// performed between an element of the source and the
	// target arrays. Each candidate pair is evaluated at
	// most once.
	equalChecks int
	// candidates is the number of move/copy candidates
	// that were inspected while looking for a value that
	// could be moved or copied.
	candidates int
	// collisions is the number of times two distinct values
	// produced the same hash. Collisions are detected by a
	// mandatory deep-equal verification, so colliding values
	// are never treated as equal.
	collisions int
}

// String implements the fmt.Stringer interface.
// Fields are always printed in a fixed order so that
// diagnostic records remain stable across runs.
func (s matchStats) String() string {
	return fmt.Sprintf(
		"hashDigests=%d equalChecks=%d candidates=%d collisions=%d",
		s.hashDigests, s.equalChecks, s.candidates, s.collisions,
	)
}

// digestFunc is the signature of the hash function used by
// the array matching pipeline. It exists as a test seam to
// simulate hash collisions deterministically.
type digestFunc func(v interface{}, sort bool) uint64

// A matchHint identifies an element that took part in an
// LCS-based array comparison. It allows the move/copy
// candidate discovery to reuse the equality result that was
// already computed by the matcher instead of evaluating the
// same candidate a second time.
type matchHint struct {
	m   *arrayMatcher
	idx int
}

// An arrayMatcher unifies the work required to compare two
// arrays: hashing, equality, longest common subsequence and
// move/copy candidate discovery.
//
// Every element of both arrays is hashed exactly once, and
// the equality of every (source, target) index pair is
// evaluated at most once. The hash is only used as a
// prefilter: a hash match is always verified with deepEqual,
// which guarantees that duplicate or colliding values cannot
// be wrongly paired.
//
// A matcher is scoped to a single array comparison, so all
// of its temporary state is released as soon as the
// comparison returns.
type arrayMatcher struct {
	stats  *matchStats
	digest digestFunc // optional, tests only

	h        hasher
	src      []interface{}
	tgt      []interface{}
	srcHash  []uint64
	tgtHash  []uint64
	eq       map[[2]int]bool
	pairs    [][2]int
	hasPairs bool
}

// newArrayMatcher returns a matcher for the given arrays.
// When stats is non-nil, diagnostic counters are reported
// to it; when digest is non-nil, it replaces the default
// hash function (test seam).
func newArrayMatcher(stats *matchStats, digest digestFunc, src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		stats:   stats,
		digest:  digest,
		src:     src,
		tgt:     tgt,
		srcHash: make([]uint64, len(src)),
		tgtHash: make([]uint64, len(tgt)),
		eq:      make(map[[2]int]bool, len(src)*len(tgt)),
	}
	for i, v := range src {
		m.srcHash[i] = m.hashValue(v)
	}
	for i, v := range tgt {
		m.tgtHash[i] = m.hashValue(v)
	}
	return m
}

// hashValue hashes v exactly once for the current
// comparison.
func (m *arrayMatcher) hashValue(v interface{}) uint64 {
	if m.stats != nil {
		m.stats.hashDigests++
	}
	if m.digest != nil {
		return m.digest(v, false)
	}
	return m.h.digest(v, false)
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// The result of a pair is evaluated once and memoized; hash
// collisions are detected and accounted for.
func (m *arrayMatcher) equal(i, j int) bool {
	key := [2]int{i, j}
	if v, ok := m.eq[key]; ok {
		return v
	}
	var eq bool
	if m.srcHash[i] == m.tgtHash[j] {
		// The hash is only a prefilter: always verify
		// equality to avoid pairing distinct values that
		// happen to share the same digest.
		if m.stats != nil {
			m.stats.equalChecks++
		}
		eq = deepEqual(m.src[i], m.tgt[j])
		if !eq && m.stats != nil {
			m.stats.collisions++
		}
	}
	m.eq[key] = eq
	return eq
}

// lcsPairs computes the index pairs of the longest common
// subsequence using the memoized, collision-safe equality
// oracle. The algorithm and its tie-breaking are identical
// to the historical lcs implementation; only the evaluation
// of candidate pairs is unified with hashing and move/copy
// discovery.
func (m *arrayMatcher) lcsPairs() [][2]int {
	if m.hasPairs {
		return m.pairs
	}
	src, tgt := m.src, m.tgt
	t := make([][]int, len(src)+1)

	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if m.equal(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := len(src), len(tgt)
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
	m.pairs, m.hasPairs = s, true
	return s
}

// failPoint identifies a stage of the array matching
// pipeline where a recoverable failure can be injected.
type failPoint uint8

const (
	failNone failPoint = iota
	// failStageLCS interrupts the comparison once the two
	// arrays are known to differ, but before the LCS is
	// computed.
	failStageLCS
	// failStageMoveCopy interrupts the comparison while
	// move/copy candidates are being discovered.
	failStageMoveCopy
)

var failPointNames = [...]string{
	"none",
	"lcs",
	"move-copy",
}

// String implements the fmt.Stringer interface.
func (p failPoint) String() string {
	if int(p) < len(failPointNames) {
		return failPointNames[p]
	}
	return "unknown"
}

// failureError is the error reported when a failure injected
// at a given stage of the comparison pipeline interrupts a
// comparison. It is recoverable: retrying the exact same
// comparison without the injection produces the regular
// result.
type failureError struct {
	stage failPoint
}

// Error implements the error interface.
func (e failureError) Error() string {
	return fmt.Sprintf("jsondiff: comparison interrupted at the %s stage", e.stage)
}

// checkFailure panics with a failureError when the given
// failure stage was requested for the comparison.
func (d *Differ) checkFailure(p failPoint) {
	if d.failAt == p {
		panic(failureError{stage: p})
	}
}

// digest hashes v and accounts the operation in the
// diagnostic counters of the Differ.
func (d *Differ) digest(v interface{}, sort bool) uint64 {
	d.stats.hashDigests++
	if d.digestFn != nil {
		return d.digestFn(v, sort)
	}
	return d.hasher.digest(v, sort)
}

// valuesEqual reports whether src and tgt are deeply equal
// and accounts the comparison in the diagnostic counters of
// the Differ.
func (d *Differ) valuesEqual(src, tgt interface{}) bool {
	d.stats.equalChecks++
	return deepEqual(src, tgt)
}

// matchDiagnostics returns the diagnostic counters collected
// during the most recent comparison, along with a boolean
// reporting whether the comparison performed any array
// matching work. When it did not, the boolean is false and
// the counters are zero: there is no measurable matching
// result for the given input instead of a guessed one.
//
// The counters describe the last comparison only; they are
// reset at the beginning of every comparison.
func (d *Differ) matchDiagnostics() (matchStats, bool) {
	if d.stats == (matchStats{}) {
		return matchStats{}, false
	}
	return d.stats, true
}

// The following methods mutate the patch while keeping the
// move-candidate ledger (d.candHints) parallel to it: every
// operation occupies exactly one hint slot, and remove
// operations store the hint of the value they removed.

// appendOp appends an operation to the patch.
func (d *Differ) appendOp(typ, from, path string, src, tgt interface{}, vl int) {
	d.patch = d.patch.append(typ, from, path, src, tgt, vl)
	d.candHints = append(d.candHints, matchHint{})
}

// removeOpAt removes the operation and its candidate hint at
// the given index.
func (d *Differ) removeOpAt(idx int) {
	d.patch = d.patch.remove(idx)
	d.candHints = append(d.candHints[:idx], d.candHints[idx+1:]...)
}

// insertOpAt inserts an operation and a blank candidate hint
// at the given position.
func (d *Differ) insertOpAt(pos int, typ, from, path string, src, tgt interface{}, vl int) {
	if pos > len(d.patch) {
		d.appendOp(typ, from, path, src, tgt, vl)
		return
	}
	d.patch = d.patch.insert(pos, typ, from, path, src, tgt, vl)
	d.candHints = append(d.candHints, matchHint{})
	copy(d.candHints[pos+1:], d.candHints[pos:len(d.candHints)-1])
	d.candHints[pos] = matchHint{}
}

// truncateOps truncates the patch and the candidate ledger
// to n operations.
func (d *Differ) truncateOps(n int) {
	d.patch = d.patch[:n]
	d.candHints = d.candHints[:n]
}
