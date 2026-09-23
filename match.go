package jsondiff

// This file unifies the responsibilities involved in matching
// the elements of two arrays:
//
//   - equality: every old/new value pair is compared at most
//     once, through a single authority (eqOracle), so that the
//     longest common subsequence and the move/copy candidate
//     discovery never maintain a parallel, diverging notion of
//     equality;
//   - hashing: digests are used exclusively as a negative
//     prefilter. Distinct hashes prove inequality without a
//     deep comparison, while equal hashes are never trusted to
//     prove equality, which keeps duplicate values safe in the
//     presence of hash collisions;
//   - longest common subsequence: lcsPairs computes the index
//     pairs on top of the oracle, and reuses the comparisons
//     resolved by the matrix during backtracking;
//   - move/copy discovery: removed elements are registered once
//     and paired with added elements through the same oracle,
//     so each candidate pair is evaluated at most once.
//
// All diagnostics are per-comparison values owned by the Differ
// that runs the comparison; they are never shared between
// concurrent comparisons and never influence the generated
// patch.

// matchDiagnostics gathers quantitative counters describing
// the work performed while matching arrays during a single
// comparison. The counters exist for diagnostics only and do
// not affect the generated patch.
type matchDiagnostics struct {
	// resolvedPairs is the number of old/new value pairs
	// whose equality was definitively resolved, whether by
	// a hash-based negative prefilter or a deep comparison.
	resolvedPairs int
	// deepComparisons is the number of pairs resolved by
	// evaluating deep equality.
	deepComparisons int
	// hashShortcuts is the number of pairs proven unequal
	// without evaluating deep equality, thanks to distinct
	// digests.
	hashShortcuts int
	// cachedPairs is the number of times an equality result
	// already resolved by an oracle was reused (LCS
	// backtracking and move/copy discovery).
	cachedPairs int
	// candidateProbes is the number of move candidate pairs
	// (removed value / added value) that had to be evaluated
	// outside of the LCS matrix.
	candidateProbes int
	// candidateHits is the number of candidate probes whose
	// equality result was already known to an oracle.
	candidateHits int
	// undecidableDigests is the number of times a digest
	// could not be relied upon, forcing a fallback to exact
	// comparisons instead of a guessed result.
	undecidableDigests int
}

// pendingRemoval carries the array-local provenance of a
// remove operation while it is being emitted.
type pendingRemoval struct {
	oracle *eqOracle
	srcIdx int
}

// pendingAdd carries the array-local provenance of an add
// operation while it is being emitted.
type pendingAdd struct {
	oracle *eqOracle
	tgtIdx int
}

// candidateEntry records a remove operation as a possible
// source for a later move operation.
type candidateEntry struct {
	// seq is the stable identity of the removal.
	seq int
	// val is the removed value, used when the removal does
	// not originate from an LCS-matched array.
	val any
	// oracle is the equality oracle of the array that
	// emitted the removal, if any.
	oracle *eqOracle
	// srcIdx is the source index of the removed element in
	// the oracle's source array.
	srcIdx int
}

// probeKey identifies a move candidate evaluation between a
// registered removed value and a value currently being added.
type probeKey struct {
	removalSeq int
	addSeq     int
}

// matchContext owns the state shared by the equality oracle,
// the LCS computation and the move/copy candidate discovery
// for the duration of one comparison.
type matchContext struct {
	stats    *matchDiagnostics
	digester func(any, bool) (uint64, bool)

	entries []*candidateEntry
	pending *pendingRemoval
	adding  *pendingAdd
	nextSeq int
	addSeq  int

	probeMem map[probeKey]bool
}

func newMatchContext(digester func(any, bool) (uint64, bool)) *matchContext {
	if digester == nil {
		digester = defaultDigest
	}
	return &matchContext{
		stats:    &matchDiagnostics{},
		digester: digester,
	}
}

// reset discards every transient state and diagnostic and
// prepares the context for a fresh comparison.
func (mc *matchContext) reset() {
	mc.stats = &matchDiagnostics{}
	mc.entries = mc.entries[:0]
	mc.pending = nil
	mc.adding = nil
	mc.nextSeq = 0
	mc.addSeq = 0
	mc.probeMem = nil
}

// beginAdd marks the start of the emission of an add
// operation. Every removed value probed while matching this
// add shares the same add sequence number, so each candidate
// pair is evaluated at most once.
func (mc *matchContext) beginAdd() {
	mc.addSeq++
}

// registerRemoval indexes the remove operation that was just
// emitted. The provenance carried by mc.pending, if any, lets
// later candidate probes reuse comparisons resolved by the
// array's LCS matrix.
func (mc *matchContext) registerRemoval(val any) int {
	seq := mc.nextSeq
	mc.nextSeq++

	e := &candidateEntry{seq: seq, val: val}
	if mc.pending != nil {
		e.oracle = mc.pending.oracle
		e.srcIdx = mc.pending.srcIdx
	}
	mc.entries = append(mc.entries, e)
	return seq
}

// findRemoval scans registered removals in emission order and
// consumes the first one whose value equals v, mirroring the
// historical behavior of scanning patch operations. It returns
// the stable identity of the consumed removal, or -1 when no
// candidate matches.
func (mc *matchContext) findRemoval(v any) int {
	for _, e := range mc.entries {
		mc.stats.candidateProbes++
		if e.oracle != nil {
			// The removal comes from an LCS-matched array.
			if mc.adding != nil && mc.adding.oracle == e.oracle {
				// Both ends belong to the same matrix, which
				// resolved the equality of every cell already.
				eq := e.oracle.matrixEqual(e.srcIdx, mc.adding.tgtIdx)
				mc.stats.candidateHits++
				if eq {
					return mc.consume(e)
				}
				continue
			}
			// The added value comes from a different array;
			// evaluate the pair once, through the oracle.
			if e.oracle.equalProbe(e.srcIdx, mc.addSeq, v) {
				return mc.consume(e)
			}
			continue
		}
		// Removal outside of any LCS-matched array: evaluate
		// the candidate pair once and memoize it.
		if eq, ok := mc.probeMem[probeKey{e.seq, mc.addSeq}]; ok {
			mc.stats.candidateHits++
			if eq {
				return mc.consume(e)
			}
			continue
		}
		mc.stats.resolvedPairs++
		if mc.hashNegative(e.val, v) {
			mc.stats.hashShortcuts++
			mc.probeMem[probeKey{e.seq, mc.addSeq}] = false
			continue
		}
		mc.stats.deepComparisons++
		eq := deepEqual(e.val, v)
		if mc.probeMem == nil {
			mc.probeMem = make(map[probeKey]bool)
		}
		mc.probeMem[probeKey{e.seq, mc.addSeq}] = eq
		if eq {
			return mc.consume(e)
		}
	}
	return -1
}

// hashNegative reports whether distinct digests prove that a
// and b are unequal. A digest is only ever used to prove
// inequality; equality is always confirmed with a deep
// comparison, so collisions cannot pair the wrong duplicates.
func (mc *matchContext) hashNegative(a, b any) bool {
	h1, ok1 := mc.digester(a, false)
	if !ok1 {
		mc.stats.undecidableDigests++
		return false
	}
	h2, ok2 := mc.digester(b, false)
	if !ok2 {
		mc.stats.undecidableDigests++
		return false
	}
	return h1 != h2
}

func (mc *matchContext) consume(e *candidateEntry) int {
	seq := e.seq
	for i, ce := range mc.entries {
		if ce == e {
			mc.entries = append(mc.entries[:i], mc.entries[i+1:]...)
			break
		}
	}
	return seq
}

// hashSlot memoizes the digest of one array element.
type hashSlot struct {
	ready bool
	ok    bool
	hash  uint64
}

// eqOracle is the single authority for the equality of the
// elements of a pair of arrays. It memoizes the result for
// every source/target index pair, and uses the configured
// digester as a sound negative prefilter.
type eqOracle struct {
	mc        *matchContext
	src       []any
	tgt       []any
	srcHashes []hashSlot
	tgtHashes []hashSlot
	matrix    map[oracleKey]bool
	probeMem  map[probeKey]bool
}

type oracleKey struct {
	i, j int
}

func (mc *matchContext) newOracle(src, tgt []any) *eqOracle {
	return &eqOracle{
		mc:        mc,
		src:       src,
		tgt:       tgt,
		srcHashes: make([]hashSlot, len(src)),
		tgtHashes: make([]hashSlot, len(tgt)),
		matrix:    make(map[oracleKey]bool, len(src)*len(tgt)),
		probeMem:  make(map[probeKey]bool),
	}
}

func (o *eqOracle) attachTarget(tgt []any) {
	o.tgt = tgt
}

// digestAt lazily computes and memoizes the digest of an
// element of either side of the matrix.
func (o *eqOracle) digestAt(side bool, idx int) (uint64, bool) {
	var s *hashSlot
	var v any
	if side { // target
		s = &o.tgtHashes[idx]
		v = o.tgt[idx]
	} else { // source
		s = &o.srcHashes[idx]
		v = o.src[idx]
	}
	if !s.ready {
		s.ready = true
		if h, ok := o.mc.digester(v, false); ok {
			s.ok = true
			s.hash = h
		}
	}
	return s.hash, s.ok
}

// hashNegative reports whether distinct digests prove that
// src[i] and tgt[j] are unequal.
func (o *eqOracle) hashNegative(i, j int) bool {
	h1, ok1 := o.digestAt(false, i)
	if !ok1 {
		o.mc.stats.undecidableDigests++
		return false
	}
	h2, ok2 := o.digestAt(true, j)
	if !ok2 {
		o.mc.stats.undecidableDigests++
		return false
	}
	return h1 != h2
}

// equalAt resolves the equality of src[i] and tgt[j], caching
// the result so that LCS backtracking and move/copy discovery
// share the exact same comparison and never recompute it.
func (o *eqOracle) equalAt(i, j int) bool {
	k := oracleKey{i, j}
	if eq, ok := o.matrix[k]; ok {
		o.mc.stats.cachedPairs++
		return eq
	}
	o.mc.stats.resolvedPairs++
	if o.hashNegative(i, j) {
		o.mc.stats.hashShortcuts++
		o.matrix[k] = false
		return false
	}
	o.mc.stats.deepComparisons++
	eq := deepEqual(o.src[i], o.tgt[j])
	o.matrix[k] = eq
	return eq
}

// matrixEqual returns the equality already resolved by the
// LCS matrix for src[i] and tgt[j].
func (o *eqOracle) matrixEqual(i, j int) bool {
	eq, ok := o.matrix[oracleKey{i, j}]
	if !ok {
		return false
	}
	return eq
}

// equalProbe resolves the equality of src[i] with an arbitrary
// value v probed while discovering move candidates. The pair
// is evaluated at most once per add.
func (o *eqOracle) equalProbe(i, addSeq int, v any) bool {
	key := probeKey{i, addSeq}
	if eq, ok := o.probeMem[key]; ok {
		o.mc.stats.candidateHits++
		return eq
	}
	o.mc.stats.resolvedPairs++
	if h1, ok1 := o.digestAt(false, i); ok1 {
		if h2, ok2 := o.mc.digester(v, false); ok2 {
			if h1 != h2 {
				o.mc.stats.hashShortcuts++
				o.probeMem[key] = false
				return false
			}
		} else {
			o.mc.stats.undecidableDigests++
		}
	} else {
		o.mc.stats.undecidableDigests++
	}
	o.mc.stats.deepComparisons++
	eq := deepEqual(o.src[i], v)
	o.probeMem[key] = eq
	return eq
}

// lcsPairs computes the index pairs of the longest common
// subsequence of src and tgt using the oracle as its only
// equality authority. Tie-breaking during backtracking is
// identical to the historical implementation, which keeps the
// generated patch stable.
func lcsPairs(mc *matchContext, src, tgt []any) [][2]int {
	if mc == nil {
		mc = newMatchContext(nil)
	}
	o := mc.newOracle(src, tgt)

	table := make([][]int, len(src)+1)
	for i := 0; i <= len(src); i++ {
		table[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(table); i++ {
		for j := 1; j < len(table[i]); j++ {
			if o.equalAt(i-1, j-1) {
				table[i][j] = table[i-1][j-1] + 1
			} else {
				table[i][j] = max(table[i-1][j], table[i][j-1])
			}
		}
	}
	i, j := len(src), len(tgt)
	pairs := make([][2]int, 0, table[i][j])

	for i > 0 && j > 0 {
		switch {
		case o.equalAt(i-1, j-1):
			pairs = append(pairs, [2]int{i - 1, j - 1})
			i--
			j--
		case table[i-1][j] > table[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i, j := 0, len(pairs)-1; i < j; i, j = i+1, j-1 {
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}
	return pairs
}

// defaultDigest is the digester used when no Differ supplies
// one, notably by the standalone lcs helper.
func defaultDigest(v any, sort bool) (uint64, bool) {
	var h hasher
	return h.digest(v, sort), true
}
