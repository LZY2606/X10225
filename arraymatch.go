package jsondiff

// matchPhase identifies a point of the array matching pipeline
// at which the diagnostic hook of a [Differ] can be invoked.
type matchPhase uint8

const (
	phaseMatcherStart matchPhase = iota // before any matching work begins
	phaseMatcherEnd                     // after all operations have been emitted
)

// matchHook is a diagnostic-only callback invoked while arrays
// are being matched. It must never be set on the production
// path; it exists solely to let the regression suite exercise
// recoverable failures at deterministic points of the pipeline.
type matchHook func(phase matchPhase, ptr pointer)

// digestHook is a diagnostic-only replacement for the element
// digest function. Every digest computed for array matching or
// factorization goes through it when it is set, so collision
// counterexamples can be exercised without forking the matching
// algorithm. When it reports equal digests for distinct values,
// the authoritative [deepEqual] predicate still decides, which
// is exactly the property the collision tests guard.
type digestHook func(val interface{}) uint64

// diagHooks groups the unexported seams used by the regression
// suite. They are not part of the public API and are never set
// by the library itself.
type diagHooks struct {
	hook   matchHook
	digest digestHook
}

// matchStats holds diagnostic counters for a single comparison.
// The counters are observational: they never influence the
// generated patch, and they are reset at the beginning of every
// [Differ.Compare] call so concurrent comparisons using
// distinct [Differ] values are fully isolated.
type matchStats struct {
	eqChecks      uint64 // equality predicate calls (hash fast paths included)
	eqEvaluations uint64 // equality calls that actually ran deepEqual
	moveProbes    uint64 // move-candidate predicate calls
	moveEvals     uint64 // move probes that actually ran deepEqual
	collisions    uint64 // equal digests rejected by deepEqual
	hashCompute   uint64 // element digests computed by the matchers
}

// arrayMatcher centralizes everything related to matching the
// elements of two arrays during a diff:
//
//   - equality: the authoritative predicate is [deepEqual];
//     digests are only used as a first-partition filter.
//   - hashing: element digests are computed lazily, at most
//     once per element, and then shared by the LCS table, the
//     unordered/equivalent comparison and copy discovery.
//   - longest common subsequence: built on top of the cached
//     equality predicate, which is evaluated at most once per
//     (src,tgt) index pair, including the backtracking pass.
//   - move/copy discovery: removed and unchanged candidates are
//     probed through memoized predicates, so each distinct pair
//     of values is compared with [deepEqual] at most once even
//     when the same candidate is probed several times.
//
// The matcher only owns per-array, per-comparison state. Nested
// arrays stack matchers and restore the previous one on release;
// a comparison that panics leaves no matcher attached to the
// [Differ].
type arrayMatcher struct {
	d      *Differ
	parent *arrayMatcher
	src    []interface{}
	tgt    []interface{}

	sh []uint64 // digests of source elements
	th []uint64 // digests of target elements

	hashed bool // sh and th have been fully computed

	// eqMemo caches the authoritative result of a deepEqual
	// evaluation between a source and a target element, keyed
	// by their indexes. Unequal digests are cached as a fast
	// negative, while equal digests are always confirmed with
	// deepEqual: digest collisions can therefore never pair
	// unequal elements.
	eqMemo map[[2]int]bool

	// moveMemo caches the result of probing a single remove
	// operation (identified by its per-comparison sequence
	// number) against a single target element index.
	moveMemo map[moveProbeKey]bool

	// copyMemo caches the digest of target element indexes, so
	// a value that is a copy candidate for several paths is
	// hashed only once.
	copyMemo map[int]uint64

	// Diagnostic counters. They are folded into the comparison
	// totals on release and never influence the generated patch.
	eqChecks      uint64
	eqEvaluations uint64
	moveProbes    uint64
	moveEvals     uint64
	collisions    uint64
	hashCompute   uint64
}

type moveProbeKey struct {
	seq uint64
	j   int
}

// newArrayMatcher builds the matcher for a single pair of
// arrays and pushes it on the matcher stack of the differ.
func (d *Differ) newArrayMatcher(src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		d:        d,
		parent:   d.matcher,
		src:      src,
		tgt:      tgt,
		sh:       make([]uint64, len(src)),
		th:       make([]uint64, len(tgt)),
		eqMemo:   make(map[[2]int]bool),
		moveMemo: make(map[moveProbeKey]bool),
		copyMemo: make(map[int]uint64),
	}
	d.matcher = m
	if d.diag.hook != nil {
		d.diag.hook(phaseMatcherStart, d.ptr)
	}
	return m
}

// newStandaloneMatcher builds a matcher that is not pushed on
// the differ stack. It is used by internal helpers that are
// invoked outside of the normal array diff traversal, so they
// cannot alter the matcher observed by any enclosing array.
func (d *Differ) newStandaloneMatcher(src, tgt []interface{}) *arrayMatcher {
	return &arrayMatcher{
		d:        d,
		src:      src,
		tgt:      tgt,
		sh:       make([]uint64, len(src)),
		th:       make([]uint64, len(tgt)),
		eqMemo:   make(map[[2]int]bool),
		moveMemo: make(map[moveProbeKey]bool),
		copyMemo: make(map[int]uint64),
	}
}

// release pops the matcher from the differ stack. It is safe to
// defer: a panic during the diff restores the parent matcher (or
// detaches the stack entirely for a top-level array), and no
// matcher state can leak into a subsequent comparison.
func (m *arrayMatcher) release() {
	if m == nil {
		return
	}
	if d := m.d; d != nil && d.matcher == m {
		d.matcher = m.parent
	}
}

// foldStats merges the matcher diagnostic counters into the
// comparison totals of its differ.
func (m *arrayMatcher) foldStats() {
	if m == nil || m.d == nil {
		return
	}
	s := &m.d.stats
	s.eqChecks += m.eqChecks
	s.eqEvaluations += m.eqEvaluations
	s.moveProbes += m.moveProbes
	s.moveEvals += m.moveEvals
	s.collisions += m.collisions
	s.hashCompute += m.hashCompute
}

// digest is the single digest entry point used by array
// matching and factorization, so production code and the
// diagnostic collision seam cannot diverge.
func (d *Differ) digest(val interface{}, sort bool) uint64 {
	if d.diag.digest != nil {
		return d.diag.digest(val)
	}
	return d.hasher.digest(val, sort)
}

func (m *arrayMatcher) ensureHashes() {
	if m.hashed {
		return
	}
	m.hashed = true
	for i, v := range m.src {
		m.sh[i] = m.d.digest(v, false)
		m.hashCompute++
	}
	for j, v := range m.tgt {
		if h, ok := m.copyMemo[j]; ok {
			m.th[j] = h
			continue
		}
		m.th[j] = m.d.digest(v, false)
		m.copyMemo[j] = m.th[j]
		m.hashCompute++
	}
}

func (m *arrayMatcher) targetHash(j int) uint64 {
	if m.hashed {
		return m.th[j]
	}
	if h, ok := m.copyMemo[j]; ok {
		return h
	}
	h := m.d.digest(m.tgt[j], false)
	m.copyMemo[j] = h
	m.hashCompute++
	return h
}

// equalAt reports whether source element i and target element j
// are deeply equal. The predicate is deterministic and memoized:
// each index pair is evaluated with deepEqual at most once for
// the whole lifetime of the matcher, including LCS backtracking.
func (m *arrayMatcher) equalAt(i, j int) bool {
	m.eqChecks++

	key := [2]int{i, j}
	if equal, ok := m.eqMemo[key]; ok {
		return equal
	}
	m.ensureHashes()
	equal := false
	if m.sh[i] == m.th[j] {
		m.eqEvaluations++
		equal = deepEqual(m.src[i], m.tgt[j])
		if !equal {
			// Equal digests for distinct values: a collision
			// that the authoritative equality predicate
			// rejected.
			m.collisions++
		}
	}
	m.eqMemo[key] = equal
	return equal
}

// lcsPairs returns the index pairs of the longest common
// subsequence. The DP fill and the backtracking share the same
// memoized equality predicate.
func (m *arrayMatcher) lcsPairs() [][2]int {
	n, q := len(m.src), len(m.tgt)
	t := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, q+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= q; j++ {
			if m.equalAt(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := n, q
	s := make([][2]int, 0, t[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.equalAt(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t[i-1][j] > t[i][j-1]:
			i--
		default:
			j--
		}
	}
	for a, b := 0, len(s)-1; a < b; a, b = a+1, b-1 {
		s[a], s[b] = s[b], s[a]
	}
	return s
}

// unorderedEqual reports whether both arrays contain the same
// elements regardless of their order, with multiplicities,
// including through nested arrays. This mirrors the historical
// "equivalent" semantics, implemented with sorted digests:
// the digest only partitions candidates, and every pairing is
// confirmed with an order-insensitive equality predicate, so
// repeated values and digest collisions can never produce a
// false match.
func (m *arrayMatcher) unorderedEqual() bool {
	return m.multisetEqual(m.src, m.tgt)
}

// multisetEqual reports whether two arrays hold the same
// elements with the same multiplicities, independently of
// their order. Sorted digests partition elements into buckets;
// each bucket pairing is verified by [unorderedEqualValue],
// which treats nested arrays as multisets as well.
func (m *arrayMatcher) multisetEqual(src, tgt []interface{}) bool {
	if len(src) != len(tgt) {
		return false
	}
	sh := make([]uint64, len(src))
	th := make([]uint64, len(tgt))
	for i, v := range src {
		sh[i] = m.d.digest(v, true)
		m.hashCompute++
	}
	for j, v := range tgt {
		th[j] = m.d.digest(v, true)
		m.hashCompute++
	}
	buckets := make(map[uint64][]int, len(src))
	for i, h := range sh {
		buckets[h] = append(buckets[h], i)
	}
	used := make([]bool, len(src))
	for j := range tgt {
		bucket := buckets[th[j]]
		matched := false
		for _, i := range bucket {
			if used[i] {
				continue
			}
			m.eqChecks++
			// Values outside the JSON type system cannot be
			// compared structurally; preserve the historical
			// digest-only verdict for such inputs.
			equal := jsonTypeSwitch(src[i]) == jsonInvalid ||
				jsonTypeSwitch(tgt[j]) == jsonInvalid ||
				m.unorderedEqualValue(src[i], tgt[j])
			if jsonTypeSwitch(src[i]) != jsonInvalid &&
				jsonTypeSwitch(tgt[j]) != jsonInvalid {
				m.eqEvaluations++
				if !equal {
					m.collisions++
				}
			}
			if equal {
				used[i] = true
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// unorderedEqualValue compares two values for the
// unordered/equivalent comparison: objects are compared by
// key/value pairs and arrays as multisets, while scalar values
// use the regular equality predicate.
func (m *arrayMatcher) unorderedEqualValue(src, tgt interface{}) bool {
	st := jsonTypeSwitch(src)
	if st != jsonTypeSwitch(tgt) {
		return false
	}
	switch st {
	case jsonArray:
		return m.multisetEqual(src.([]interface{}), tgt.([]interface{}))
	case jsonObject:
		oobj := src.(map[string]interface{})
		nobj := tgt.(map[string]interface{})
		if len(oobj) != len(nobj) {
			return false
		}
		for k, v1 := range oobj {
			v2, ok := nobj[k]
			if !ok || !m.unorderedEqualValue(v1, v2) {
				return false
			}
		}
		return true
	default:
		return deepEqual(src, tgt)
	}
}

// probeRemoved compares the value of a remove operation against
// target element j for move factorization. The result is cached
// per (remove sequence, target index), so repeated lookups of
// the same candidate never compare the values twice.
func (m *arrayMatcher) probeRemoved(op Operation, j int) bool {
	m.moveProbes++
	key := moveProbeKey{seq: op.moveSeq, j: j}
	if equal, ok := m.moveMemo[key]; ok {
		return equal
	}
	m.moveEvals++
	equal := deepEqual(op.OldValue, m.tgt[j])
	m.moveMemo[key] = equal
	return equal
}

// findRemoved locates the first live remove operation of the
// patch whose old value is deeply equal to target element j.
// The scan order and tie-breaking are identical to the
// historical implementation (which matters with duplicated
// values); the only change is that comparisons go through the
// matcher memo and are therefore evaluated once per candidate.
func (m *arrayMatcher) findRemoved(j int) int {
	if m == nil {
		return -1
	}
	patch := m.d.patch
	for i := 0; i < len(patch); i++ {
		op := patch[i]
		if op.Type != OperationRemove {
			continue
		}
		if m.probeRemoved(op, j) {
			return i
		}
	}
	return -1
}

// findUnchanged returns the pointer of an unchanged value of
// the prepared document that is deeply equal to target element j
// and can therefore be referenced by a copy operation. The
// digest partitions candidates, but equality is always confirmed
// with deepEqual: on a digest collision the location is
// considered unknown and an add operation is emitted instead of
// guessing a copy source.
func (m *arrayMatcher) findUnchanged(j int) string {
	hashmap := m.d.hashmap
	if len(hashmap) == 0 {
		return emptyPointer
	}
	node, exists := hashmap[m.targetHash(j)]
	if !exists {
		return emptyPointer
	}
	if !deepEqual(node.val, m.tgt[j]) {
		m.collisions++
		return emptyPointer
	}
	return node.ptr
}
