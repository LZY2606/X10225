package jsondiff

// Array matching: unified equality, hashing, LCS and
// move/copy candidate accounting.
//
// The valueInterner is created once per Compare call and owns
// every service involved in array matching:
//
//   - value digesting: each value matched by the pipeline is
//     hashed once,
//   - equality: deep comparisons are memoized by ordered token
//     pair, so a candidate pair is compared at most once,
//   - move discovery: removed values are registered in emission
//     order and resolved through digest buckets,
//   - copy discovery: unchanged locations are kept per digest.
//
// The LCS table and the move/copy resolution therefore share
// the same equality verdicts: digests only pre-filter candidate
// pairs, a hash collision never pairs values, and deep equality
// remains the single source of truth.

// MatchStats holds diagnostic counters collected during a
// single Compare call. The counters are reset at the start of
// every call; they never influence the generated patch and are
// never shared between concurrent comparisons.
type MatchStats struct {
	// HashComputed is the number of value digests computed by
	// the matching pipeline.
	HashComputed int
	// ElementComparisons is the number of ordered element
	// pairs whose equality has been evaluated, whether the
	// verdict came from the memo or from a deep comparison.
	ElementComparisons int
	// CacheHits is the number of element comparisons whose
	// verdict was already memoized.
	CacheHits int
	// DeepComparisons is the number of element comparisons
	// actually evaluated through deep equality.
	DeepComparisons int
	// HashCollisions counts non-equal values that share a
	// digest bucket.
	HashCollisions int
	// ArraysMatched is the number of arrays matched with LCS.
	ArraysMatched int
	// MoveCandidates is the number of removed values consumed
	// to generate a move operation.
	MoveCandidates int
	// CopyCandidates is the number of unchanged locations
	// consumed to generate a copy operation.
	CopyCandidates int
}

// matchVerdict is the tri-state result of an element equality
// evaluation.
type matchVerdict int8

const (
	matchVerdictUnequal matchVerdict = iota - 1
	matchVerdictUnknown
	matchVerdictEqual
)

// matchHooks contains test-only seams used by the regression
// suite for the unified array matching pipeline. The zero value
// is a no-op and production code never sets it; hooks exercise
// the production matching code rather than a parallel algorithm.
type matchHooks struct {
	// digest overrides the digest of a value. Returning false
	// leaves the production digest untouched. Overrides must
	// remain sound (equal values get equal digests); the suite
	// uses this only to synthesize collisions between distinct
	// values.
	digest func(v any) (uint64, bool)
	// equal may force an equality verdict for an element pair.
	// matchVerdictUnknown leaves the decision to production code.
	equal func(a, b any) matchVerdict
	// stage is invoked at well-defined stages while an array is
	// matched; returning an error injects a failure. The internal
	// errMatchAbort triggers a transparent, stateless restart of
	// the whole comparison; errUnsupportedMatch surfaces through
	// the public error hierarchy.
	stage func(s string) error
}

const (
	stageBeforeIntern = "before_intern"
	stageBeforeLCS    = "before_lcs"
	stageAfterLCS     = "after_lcs"
)

// removedValue is a move candidate registered in emission
// order; it mirrors a remove operation in the patch.
type removedValue struct {
	path     string
	token    int
	class    int
	consumed bool
}

// valueInterner owns per-Compare matching state.
type valueInterner struct {
	d *Differ

	tokens  []any
	classes []int

	// buckets[digest] holds token ids sharing that digest, in
	// first-seen order.
	buckets map[uint64][]int

	// memo caches ordered equality verdicts between token
	// pairs; keys are produced with pairKey.
	memo map[uint64]matchVerdict

	// removed holds move candidates in emission order.
	removed []removedValue

	// overflow stores copy candidates displaced from the
	// single-slot d.hashmap when distinct classes share a
	// digest, keyed by digest in insertion order.
	overflow map[uint64][]jsonNode

	stats MatchStats
}

func newInterner(d *Differ) *valueInterner {
	return &valueInterner{
		d:        d,
		buckets:  make(map[uint64][]int),
		memo:     make(map[uint64]matchVerdict),
		overflow: make(map[uint64][]jsonNode),
	}
}

func pairKey(a, b int) uint64 {
	if a > b {
		a, b = b, a
	}
	return uint64(uint32(a))<<32 | uint64(uint32(b))
}

// savepoint captures the part of the interner state owned by a
// single array match, so an aborted match can be rewound.
type internerSavepoint struct {
	tokens  int
	removed int
}

func (in *valueInterner) save() internerSavepoint {
	return internerSavepoint{
		tokens:  len(in.tokens),
		removed: len(in.removed),
	}
}

// digestOf returns the digest of a value, honoring the
// test-only digest hook.
func (in *valueInterner) digestOf(v any) uint64 {
	if h := in.d.matchHooks; h != nil && h.digest != nil {
		if hh, ok := h.digest(v); ok {
			return hh
		}
	}
	return in.d.hasher.digest(v, false)
}

// intern registers a value and returns its token and
// equivalence class. Equal values share a class; values whose
// digest collides without being equal get distinct classes and
// bump HashCollisions.
func (in *valueInterner) intern(v any) (token, class int) {
	in.stats.HashComputed++
	h := in.digestOf(v)
	tok := len(in.tokens)
	in.tokens = append(in.tokens, v)

	group := in.buckets[h]
	class = -1
	for _, other := range group {
		if in.equalTokens(tok, other) == matchVerdictEqual {
			class = in.classes[other]
			break
		}
	}
	if class == -1 {
		class = len(in.classes)
		in.classes = append(in.classes, class)
		if len(group) != 0 {
			in.stats.HashCollisions++
		}
	}
	in.buckets[h] = append(group, tok)
	return tok, class
}

// equalTokens reports the equality verdict of two token ids.
// Every unordered pair is evaluated at most once.
func (in *valueInterner) equalTokens(a, b int) matchVerdict {
	if a == b {
		return matchVerdictEqual
	}
	key := pairKey(a, b)
	in.stats.ElementComparisons++
	if v, ok := in.memo[key]; ok {
		in.stats.CacheHits++
		return v
	}

	verdict := matchVerdictUnequal
	if in.d.matchHooks != nil && in.d.matchHooks.equal != nil {
		if v := in.d.matchHooks.equal(in.tokens[a], in.tokens[b]); v != matchVerdictUnknown {
			in.memo[key] = v
			return v
		}
	}
	if deepEqual(in.tokens[a], in.tokens[b]) {
		verdict = matchVerdictEqual
	}
	in.stats.DeepComparisons++
	in.memo[key] = verdict
	return verdict
}

// internArray registers every element of src and tgt exactly
// once and returns their token ids and equivalence classes.
func (in *valueInterner) internArray(src, tgt []any) (srcTok, srcClass, tgtTok, tgtClass []int) {
	srcTok = make([]int, len(src))
	srcClass = make([]int, len(src))
	tgtTok = make([]int, len(tgt))
	tgtClass = make([]int, len(tgt))
	for i, v := range src {
		srcTok[i], srcClass[i] = in.intern(v)
	}
	for i, v := range tgt {
		tgtTok[i], tgtClass[i] = in.intern(v)
	}
	return srcTok, srcClass, tgtTok, tgtClass
}

// registerRemoved records a removed value as a move candidate.
func (in *valueInterner) registerRemoved(path string, v any) {
	tok, class := in.intern(v)
	in.removed = append(in.removed, removedValue{
		path:  path,
		token: tok,
		class: class,
	})
}

// liveOrdinal maps a ledger index to its ordinal among the
// remove operations still present in the patch.
func (in *valueInterner) liveOrdinal(i int) int {
	pos := i
	for j := 0; j < i; j++ {
		if in.removed[j].consumed {
			pos--
		}
	}
	return pos
}

// findRemovedIndex mirrors Differ.findRemoved: it returns the
// patch index of the earliest not-yet-consumed remove whose
// value is equal to v. The candidate is marked consumed only by
// consumeRemovedAt, once the caller accepted the move (a path
// prefix conflict leaves it available, as in the legacy scan).
func (in *valueInterner) findRemovedIndex(patch Patch, v any) int {
	if len(in.removed) == 0 {
		return -1
	}
	tok, class := in.intern(v)
	for i := range in.removed {
		r := &in.removed[i]
		if r.consumed || r.class != class {
			continue
		}
		if in.equalTokens(r.token, tok) != matchVerdictEqual {
			continue
		}
		want := in.liveOrdinal(i)
		ordinal := 0
		for idx, op := range patch {
			if op.Type != OperationRemove {
				continue
			}
			if ordinal == want {
				return idx
			}
			ordinal++
		}
	}
	return -1
}

// consumeRemovedAt marks the ledger candidate backing the remove
// op at patch index idx as consumed.
func (in *valueInterner) consumeRemovedAt(patch Patch, idx int) {
	ordinal := 0
	for i, op := range patch {
		if i == idx {
			break
		}
		if op.Type == OperationRemove {
			ordinal++
		}
	}
	live := 0
	for i := range in.removed {
		if in.removed[i].consumed {
			continue
		}
		if live == ordinal {
			in.removed[i].consumed = true
			in.stats.MoveCandidates++
			return
		}
		live++
	}
}

// registerUnchanged records an unchanged location as a copy
// candidate. The primary slot keeps the legacy last-wins
// storage; colliding digests spill into overflow, whose
// ordering makes source selection deterministic.
func (in *valueInterner) registerUnchanged(h uint64, node jsonNode) {
	cur, ok := in.d.hashmap[h]
	if !ok {
		in.d.hashmap[h] = node
		return
	}
	if in.nodesEqual(cur, node) {
		in.d.hashmap[h] = node // last-wins for equal values
		return
	}
	in.stats.HashCollisions++
	in.overflow[h] = append(in.overflow[h], cur, node)
	delete(in.d.hashmap, h)
}

func (in *valueInterner) nodesEqual(a, b jsonNode) bool {
	ta, _ := in.intern(a.val)
	tb, _ := in.intern(b.val)
	return in.equalTokens(ta, tb) == matchVerdictEqual
}

// findUnchanged resolves a copy candidate by digest, verifying
// equality so colliding digests never pair distinct values. The
// primary slot wins; overflow candidates follow insertion order.
func (in *valueInterner) findUnchanged(h uint64, v any) string {
	tok, _ := in.intern(v)
	if node, ok := in.d.hashmap[h]; ok {
		nt, _ := in.intern(node.val)
		if in.equalTokens(tok, nt) == matchVerdictEqual {
			in.stats.CopyCandidates++
			return node.ptr
		}
		in.stats.HashCollisions++
	}
	for _, node := range in.overflow[h] {
		nt, _ := in.intern(node.val)
		if in.equalTokens(tok, nt) == matchVerdictEqual {
			in.stats.CopyCandidates++
			return node.ptr
		}
	}
	return emptyPointer
}
