package jsondiff

// eqOracle is the single equality service shared by every
// stage of an array comparison:
//
//   - the LCS length table and its traceback,
//   - the unordered-equivalence check,
//   - the discovery of move/copy candidates against the
//     operations already emitted.
//
// It owns two responsibilities:
//
//  1. Interning — mapping every candidate value to a small
//     integer id. Buckets are keyed by the value digest, but
//     bucket membership is always confirmed with deep
//     equality through pairEqual: hash collisions can never
//     identify two distinct values, and repeated values
//     share the same id.
//  2. Memoizing candidates — the result of every old/new
//     equality candidate is evaluated at most once per
//     comparison; every other consumer reuses the answer.
//
// A fresh oracle is created for every call to
// Differ.Compare, which makes all diagnostic counters and
// caches local to the comparison: concurrent comparisons
// never share state, and once a comparison (successful or
// failed) is over, nothing it computed survives into the
// next one.
type eqOracle struct {
	d *Differ

	// ids maps a digest bucket to the value ids that hash
	// to it. Membership in a bucket is necessary but never
	// sufficient: equality is always confirmed.
	ids map[uint64][]int
	// vals holds one representative value per id.
	vals []interface{}
	// results caches the equality verdict of an unordered
	// pair of value ids: eqEqual or eqDiffer.
	results map[[2]int]uint8

	stats *matchStats
}

func newEqOracle(d *Differ) *eqOracle {
	return &eqOracle{
		d:       d,
		ids:     make(map[uint64][]int),
		results: make(map[[2]int]uint8),
		stats:   &d.diag,
	}
}

// digest computes the digest of v. The "at most once per
// distinct position" guarantee is provided by the callers
// (the matcher caches per-position digests); the oracle
// itself never uses a map keyed by values, since JSON
// arrays and objects are not comparable Go map keys.
func (o *eqOracle) digest(v interface{}, sort bool) uint64 {
	o.stats.digests++
	if o.d.testDigest != nil {
		return o.d.testDigest(v)
	}
	return o.d.hasher.digest(v, sort)
}

// intern returns the id of v: the id of an existing value
// deeply equal to v if one exists in the same digest
// bucket, or a freshly allocated id. The digest only routes
// to the candidate bucket; every candidate is confirmed (or
// rejected) through the memoized equality predicate.
func (o *eqOracle) intern(v interface{}, digest uint64) int {
	for _, id := range o.ids[digest] {
		if o.pairEqual(id, len(o.vals)) {
			return id
		}
	}
	id := len(o.vals)
	o.vals = append(o.vals, v)
	o.ids[digest] = append(o.ids[digest], id)
	return id
}

// internValue digests v and interns it. The sort flag only
// selects order-sensitive/insensitive hashing; it does not
// change the verdict, which always comes from deep equality.
func (o *eqOracle) internValue(v interface{}, sort bool) int {
	return o.intern(v, o.digest(v, sort))
}

// pairEqual reports whether the two values represented by
// their (already interned) ids are deeply equal. The
// predicate is evaluated at most once for a given unordered
// pair of ids; later requests reuse the cached verdict.
func (o *eqOracle) pairEqual(a, b int) bool {
	if a == b {
		o.stats.candidateReuse++
		return true
	}
	key := [2]int{a, b}
	if a > b {
		key = [2]int{b, a}
	}
	switch o.results[key] {
	case eqEqual:
		o.stats.candidateReuse++
		return true
	case eqDiffer:
		o.stats.candidateReuse++
		return false
	}

	o.stats.comparisons++
	if deepEqual(o.vals[a], o.vals[b]) {
		o.results[key] = eqEqual
		return true
	}
	o.results[key] = eqDiffer
	return false
}

// equal reports whether an old and a new candidate value
// are deeply equal. The digests only route interning; the
// verdict always comes from a collision-safe, memoized deep
// equality evaluation.
func (o *eqOracle) equal(oldv, newv interface{}, oldHash, newHash uint64) bool {
	oi := o.intern(oldv, oldHash)
	ni := o.intern(newv, newHash)
	return o.pairEqual(oi, ni)
}

// equalValues is used by the move/copy discovery performed
// while emitting operations. It uses the same candidate
// memoization as the LCS matrix, so a value pair discovered
// first during the patch scan is never re-evaluated, and
// hash collisions cannot produce a wrong move/copy.
func (o *eqOracle) equalValues(oldv, newv interface{}) bool {
	oi := o.internValue(oldv, false)
	ni := o.internValue(newv, false)
	return o.pairEqual(oi, ni)
}
