package jsondiff

import "strings"

// arrayMatchStats contains diagnostic counters produced while
// matching the arrays of a single comparison invocation.
// The counters are purely diagnostic: they never influence the
// generated patch, and they are reset at the start of every
// comparison so that concurrent comparisons on distinct Differ
// instances are fully isolated.
type arrayMatchStats struct {
	// ElementComparisons is the number of source/target element
	// pairs whose equality was actually evaluated through
	// deepEqualValue. Pairs resolved from a cached result, or
	// rejected by a distinct structural digest, are not counted.
	ElementComparisons int64

	// HashedElements is the number of distinct (by position)
	// array elements for which a structural digest was computed.
	HashedElements int64

	// MoveCandidates counts candidates that were promoted to a
	// move operation during factorization.
	MoveCandidates int64

	// CopyCandidates counts candidates that were promoted to a
	// copy operation during factorization.
	CopyCandidates int64
}

// eqState is the tristate result of an equality evaluation
// between a source and a target element.
type eqState int8

const (
	eqUnknown eqState = iota
	eqEqual
	eqUnequal
)

// equalityOracle is a memoized n*m matrix of equality results
// between the elements of a source and a target array. Each
// pair is evaluated at most once: the LCS dynamic-programming
// fill and the backtrack/move/copy discovery share the same
// cells.
type equalityOracle struct {
	cells []eqState
	width int
}

func newEqualityOracle(n, m int) equalityOracle {
	return equalityOracle{
		cells: make([]eqState, n*m),
		width: m,
	}
}

// equals reports whether src[i] and tgt[j] are deeply equal.
// The structural digest is used solely as a negative prefilter:
// a digest mismatch proves inequality, while equal digests are
// always confirmed with a deep equality check. A digest
// collision can therefore never pair unequal elements.
func (o *equalityOracle) equals(m *arrayMatcher, i, j int) bool {
	idx := i*o.width + j
	switch o.cells[idx] {
	case eqEqual:
		return true
	case eqUnequal:
		return false
	}

	src, tgt := m.src[i], m.tgt[j]
	result := eqUnequal

	switch {
	case m.d == nil:
		// Standalone invocation (no hasher available).
		if deepEqual(src, tgt) {
			result = eqEqual
		}
	case jsonTypeSwitch(src) == jsonInvalid || jsonTypeSwitch(tgt) == jsonInvalid:
		// Invalid JSON types must go through deepEqual so that
		// the existing error model (a panic converted to an
		// error by CompareWithoutMarshal) is preserved.
		if deepEqual(src, tgt) {
			result = eqEqual
		}
	case m.srcHash(i) != m.tgtHash(j):
		// Distinct structural digests: provably unequal.
	default:
		if deepEqual(src, tgt) {
			result = eqEqual
		}
	}

	o.cells[idx] = result
	if m.d != nil {
		m.stats.ElementComparisons++
		if m.d.failAfter > 0 && m.stats.ElementComparisons >= int64(m.d.failAfter) {
			panic(errFailpoint)
		}
	}
	return result == eqEqual
}

// arrayMatcher owns, for the comparison of a single pair of
// arrays, the equality oracle, the per-element structural
// digests and the bookkeeping required to discover move/copy
// candidates. A matcher is created per array node; nested
// arrays are linked through parent and do not share state.
type arrayMatcher struct {
	d      *Differ
	parent *arrayMatcher

	src    []interface{}
	tgt    []interface{}
	prefix string

	oracle equalityOracle

	// Per-index digests, computed lazily at most once.
	srcHash    []uint64
	tgtHash    []uint64
	srcHashSet []bool
	tgtHashSet []bool

	// src indices of the direct-child remove operations that
	// are still present in the patch, in emission order.
	pendingRemoves []int

	stats arrayMatchStats
}

// beginArrayMatch starts the matching of a source and target
// array located at the given (already materialized) pointer
// prefix. Nested invocations are chained through parent.
func (d *Differ) beginArrayMatch(src, tgt []interface{}, prefix string) *arrayMatcher {
	m := &arrayMatcher{
		d:      d,
		parent: d.matcher,
		src:    src,
		tgt:    tgt,
		prefix: prefix,
	}
	d.matcher = m
	return m
}

// finish aggregates the matcher counters into the comparison
// diagnostics and restores the parent matcher.
func (d *Differ) finishArrayMatch(m *arrayMatcher) {
	d.stats.ElementComparisons += m.stats.ElementComparisons
	d.stats.HashedElements += m.stats.HashedElements
	d.stats.MoveCandidates += m.stats.MoveCandidates
	d.stats.CopyCandidates += m.stats.CopyCandidates
	d.matcher = m.parent
}

func (m *arrayMatcher) srcHashAt(i int) uint64 {
	if m.srcHash == nil {
		m.srcHash = make([]uint64, len(m.src))
		m.srcHashSet = make([]bool, len(m.src))
	}
	if !m.srcHashSet[i] {
		m.srcHash[i] = m.d.hasher.digest(m.src[i], false)
		m.srcHashSet[i] = true
		m.stats.HashedElements++
	}
	return m.srcHash[i]
}

func (m *arrayMatcher) tgtHashAt(j int) uint64 {
	if m.tgtHash == nil {
		m.tgtHash = make([]uint64, len(m.tgt))
		m.tgtHashSet = make([]bool, len(m.tgt))
	}
	if !m.tgtHashSet[j] {
		m.tgtHash[j] = m.d.hasher.digest(m.tgt[j], false)
		m.tgtHashSet[j] = true
		m.stats.HashedElements++
	}
	return m.tgtHash[j]
}

func (m *arrayMatcher) recordRemove(srcIdx int) {
	m.pendingRemoves = append(m.pendingRemoves, srcIdx)
}

// isDirectChild reports whether path points at a direct element
// of the matched array, as opposed to a nested location.
func (m *arrayMatcher) isDirectChild(path string) bool {
	if !strings.HasPrefix(path, m.prefix) {
		return false
	}
	rest := path[len(m.prefix):]
	if len(rest) < 2 || rest[0] != separator {
		return false
	}
	for i := 1; i < len(rest); i++ {
		if rest[i] == separator {
			return false
		}
	}
	return true
}

// findRemoved mirrors Differ.findRemoved scan order, but direct
// children of the matched array are resolved through the shared
// equality oracle, so each source/target candidate pair is
// evaluated at most once. Remove operations emitted outside the
// array (earlier locations or nested arrays) keep the original
// deepEqual semantics and retain their precedence by patch
// position.
func (m *arrayMatcher) findRemoved(v interface{}, tgtIdx int) (int, bool) {
	d := m.d
	pending := 0

	for i := 0; i < len(d.patch); i++ {
		op := d.patch[i]
		if op.Type != OperationRemove {
			continue
		}
		if m.isDirectChild(op.Path) {
			srcIdx := m.pendingRemoves[pending]
			pending++
			if m.oracle.equals(m, srcIdx, tgtIdx) {
				m.pendingRemoves = append(m.pendingRemoves[:pending-1], m.pendingRemoves[pending:]...)
				return i, true
			}
			continue
		}
		if deepEqual(op.OldValue, v) {
			return i, true
		}
	}
	return -1, false
}

// lcsPairs returns the LCS index pairs using the shared
// equality oracle. Tie-breaking is identical to the historical
// standalone lcs implementation.
func (m *arrayMatcher) lcsPairs() [][2]int {
	n, k := len(m.src), len(m.tgt)
	m.oracle = newEqualityOracle(n, k)

	t := make([][]int, n+1)
	for i := range t {
		t[i] = make([]int, k+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= k; j++ {
			if m.oracle.equals(m, i-1, j-1) {
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
		case m.oracle.equals(m, i-1, j-1):
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

// addMatched is the factorization-aware add path for elements
// discovered by the array matcher. It reuses the shared oracle
// and digests to account for move/copy candidates.
func (d *Differ) addMatched(m *arrayMatcher, path string, v interface{}, tgtIdx int, doc string) {
	if !d.opts.factorize {
		d.patch = d.patch.append(OperationAdd, emptyPointer, path, nil, v, 0)
		return
	}

	if idx, ok := m.findRemoved(v, tgtIdx); ok {
		op := d.patch[idx]

		// https://tools.ietf.org/html/rfc6902#section-4.4f
		if !hasProperPathPrefix(op.Path, path) {
			d.patch = d.patch.remove(idx)
			d.patch = d.patch.insert(d.snapshotPatchLen, OperationMove, op.Path, path, v, v, 0)
			m.stats.MoveCandidates++
		}
		return
	}

	uptr := d.findUnchangedHash(m.tgtHashAt(tgtIdx), v)
	if len(uptr) != 0 && !d.opts.invertible {
		d.patch = d.patch.append(OperationCopy, uptr, path, nil, v, 0)
		m.stats.CopyCandidates++
	} else {
		d.patch = d.patch.append(OperationAdd, emptyPointer, path, nil, v, len(doc))
	}
}
