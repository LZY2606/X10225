package jsondiff

import (
	"encoding/json"
	"reflect"
	"sort"
)

// This file unifies the responsibilities involved when two
// JSON values are compared for array matching:
//
//   - value identity and deep equality,
//   - digest hashing (used strictly as a prefilter),
//   - the longest common subsequence computation, and
//   - the discovery of move/copy operation candidates.
//
// A single compareRun instance is created for every public
// comparison entry point (Compare/CompareJSON/
// CompareWithoutMarshal). It owns the memoization tables,
// the diagnostic counters and the move/copy candidate
// ledger, so that:
//
//   - a candidate pair of values is ever deep-compared once,
//   - a digest is ever computed once for a given value,
//   - hashes alone never establish equality: every hash hit
//     is confirmed with deepEqual, which makes duplicate
//     values and hash collisions behave correctly, and
//   - diagnostics never escape the comparison that produced
//     them: the state is per-call, recreated on every entry
//     and therefore isolated for concurrent comparisons
//     performed on distinct Differ instances.

// eqState is the tri-state result of an equality probe.
type eqState uint8

const (
	eqUnknown eqState = iota
	eqEqual
	eqNotEqual
)

// valueIdentity identifies values for memoization.
// Scalar JSON values are identified by their semantic value,
// so that two occurrences of the same scalar share a single
// identity and are never compared. Composite values (arrays
// and objects) are identified by their pointer, since their
// content is mutable with respect to the Equivalent option.
type valueIdentity struct {
	kind  jsonValueType
	num   float64
	str   string
	b     bool
	isNil bool
	ptr   uintptr
}

// digestKey memoizes digest results per value and "sort"
// mode combination.
type digestKey struct {
	id   valueIdentity
	sort bool
}

// removedEntry records a remove operation that may later be
// reused as the source of a move operation.
type removedEntry struct {
	opSeq    int64
	hash     uint64
	valID    valueIdentity
	consumed bool
}

// unchangedNode is one of the locations indexed in the
// factorize index that may be the source of a copy.
type unchangedNode struct {
	node jsonNode
	id   valueIdentity
}

// compareRun owns all the state created for a single public
// comparison call.
type compareRun struct {
	// eq memoizes the outcome of deep comparisons between
	// two value identities. The key is order-independent.
	eq map[[2]valueIdentity]eqState

	// digest caches the digest computed for every value,
	// keyed by identity and sort mode.
	digest map[digestKey]uint64

	// removals is the ledger of remove operations that are
	// candidates for move operations.
	removals []removedEntry

	// unchanged is the set of locations that are candidates
	// for copy operations, grouped by digest hash.
	unchanged map[uint64][]unchangedNode

	// ids records every distinct identity observed during
	// the comparison.
	ids map[valueIdentity]struct{}

	stats MatchStats

	nextOpSeq int64
}

func (d *Differ) beginRun() {
	run := &compareRun{
		eq:        make(map[[2]valueIdentity]eqState),
		digest:    make(map[digestKey]uint64),
		unchanged: make(map[uint64][]unchangedNode),
		ids:       make(map[valueIdentity]struct{}),
	}
	// The factorize index is only meaningful within the
	// scope of a single comparison. Clearing it here also
	// guarantees that a failed comparison cannot leave
	// stale entries visible to a subsequent, successful one.
	for k := range d.hashmap {
		delete(d.hashmap, k)
	}
	if d.hashmap == nil {
		d.hashmap = make(map[uint64]jsonNode)
	}
	d.run = run
}

// identityOf returns the identity of v.
func (r *compareRun) identityOf(v interface{}) valueIdentity {
	id := valueIdentityOf(v)
	r.ids[id] = struct{}{}
	return id
}

// valueIdentityOf computes the identity of a JSON value.
func valueIdentityOf(v interface{}) valueIdentity {
	switch t := v.(type) {
	case nil:
		return valueIdentity{kind: jsonNull, isNil: true}
	case string:
		return valueIdentity{kind: jsonString, str: t}
	case bool:
		return valueIdentity{kind: jsonBoolean, b: t}
	case float64:
		return valueIdentity{kind: jsonNumberFloat, num: t}
	case json.Number:
		return valueIdentity{kind: jsonNumberString, str: t.String()}
	case []interface{}:
		return valueIdentity{kind: jsonArray, ptr: reflect.ValueOf(t).Pointer()}
	case map[string]interface{}:
		return valueIdentity{kind: jsonObject, ptr: reflect.ValueOf(t).Pointer()}
	default:
		return valueIdentity{kind: jsonInvalid}
	}
}

// equal returns whether the two values are deeply equal,
// memoizing the result for the lifetime of the run. It is
// the single predicate used by both the array matcher and the
// move/copy candidate discovery, which guarantees that any
// pair of values is ever effectively compared once.
func (r *compareRun) equal(a, b interface{}) bool {
	ia, ib := r.identityOf(a), r.identityOf(b)
	return r.equalIDs(a, b, ia, ib)
}

// equalIDs reports whether two values whose identities are
// already known are deeply equal.
func (r *compareRun) equalIDs(a, b interface{}, ia, ib valueIdentity) bool {
	if ia == ib {
		// Identical scalar values, or a composite value
		// compared with itself.
		return true
	}
	key := normalizeKey(ia, ib)
	if s, ok := r.eq[key]; ok {
		return s == eqEqual
	}
	r.stats.EqualComparisons++
	eq := deepEqual(a, b)
	if eq {
		r.eq[key] = eqEqual
	} else {
		r.eq[key] = eqNotEqual
	}
	return eq
}

// normalizeKey returns an order-independent memoization key.
func normalizeKey(a, b valueIdentity) [2]valueIdentity {
	if compareIdentity(a, b) > 0 {
		return [2]valueIdentity{b, a}
	}
	return [2]valueIdentity{a, b}
}

// compareIdentity imposes a deterministic total order on
// value identities, used solely to normalize memo keys.
func compareIdentity(a, b valueIdentity) int {
	if a.kind != b.kind {
		return int(a.kind) - int(b.kind)
	}
	switch a.kind {
	case jsonNull:
		return 0
	case jsonString, jsonNumberString:
		switch {
		case a.str < b.str:
			return -1
		case a.str > b.str:
			return 1
		}
	case jsonBoolean:
		if a.b == b.b {
			return 0
		}
		if !a.b {
			return -1
		}
		return 1
	case jsonNumberFloat:
		switch {
		case a.num < b.num:
			return -1
		case a.num > b.num:
			return 1
		}
	case jsonArray, jsonObject:
		switch {
		case a.ptr < b.ptr:
			return -1
		case a.ptr > b.ptr:
			return 1
		}
	}
	return 0
}

// digestOf returns the digest of v, computing and caching
// it on first use. Hashes are only ever used to short-circuit
// unequal pairs; equality is always confirmed with equal.
func (r *compareRun) digestOf(d *Differ, v interface{}, sort bool) uint64 {
	id := r.identityOf(v)
	key := digestKey{id: id, sort: sort}
	if h, ok := r.digest[key]; ok {
		return h
	}
	r.stats.DigestComputations++
	var h uint64
	if d.digestHook != nil {
		h = d.digestHook(v, sort)
	} else {
		h = d.hasher.digest(v, sort)
	}
	r.digest[key] = h
	return h
}

// MatchStats exposes quantifiable, diagnostic-only counters
// about the work performed by a single comparison. The zero
// value is valid and the counters never influence the
// generated patch.
type MatchStats struct {
	// EqualComparisons is the number of times deep equality
	// was effectively evaluated between a pair of distinct
	// values. Memoized pairs are not counted again.
	EqualComparisons int
	// DigestComputations is the number of digest hashes that
	// were effectively computed. Cached digests are not
	// counted again.
	DigestComputations int
	// LCSProbes is the number of matrix cells for which
	// equality was consulted while computing longest common
	// subsequences.
	LCSProbes int
	// MoveCandidateComparisons is the number of hash-prefixed
	// candidate examinations performed while pairing add
	// operations with previously removed values.
	MoveCandidateComparisons int
	// CopyCandidateComparisons is the number of hash-prefixed
	// candidate examinations performed while resolving a copy
	// source.
	CopyCandidateComparisons int
}

// matchDiagnostics returns a snapshot of the diagnostic
// counters of the most recent comparison. It is intended for
// diagnostics and tests only; it does not expose internal
// memoization tables or temporary state.
func (d *Differ) matchDiagnostics() MatchStats {
	if d.run == nil {
		return MatchStats{}
	}
	return d.run.stats
}

// nextSeq reserves a unique, monotonically increasing
// operation sequence number within the run.
func (r *compareRun) nextSeq() int64 {
	r.nextOpSeq++
	return r.nextOpSeq
}

// registerRemoval indexes a remove operation identified by
// its sequence number as a move candidate.
func (r *compareRun) registerRemoval(d *Differ, opSeq int64, v interface{}) {
	r.removals = append(r.removals, removedEntry{
		opSeq: opSeq,
		hash:  r.digestOf(d, v, false),
		valID: r.identityOf(v),
	})
}

// matchRemoved returns the index, within the public patch,
// of the first non-consumed remove operation whose old value
// is deeply equal to v. Hash collisions and duplicate values
// are resolved with the run-wide equality memo, so each
// candidate pair is compared at most once.
func (r *compareRun) matchRemoved(d *Differ, v interface{}) int {
	id := r.identityOf(v)
	h := r.digestOf(d, v, false)
	for i := range r.removals {
		e := &r.removals[i]
		if e.consumed || e.hash != h {
			continue
		}
		r.stats.MoveCandidateComparisons++
		idx := d.indexOfOpSeq(e.opSeq)
		if idx < 0 {
			continue
		}
		if r.equalIDs(d.patch[idx].OldValue, v, e.valID, id) {
			return idx
		}
	}
	return -1
}

// consumeRemoved marks the remove operation identified by
// opSeq as consumed by a move operation.
func (r *compareRun) consumeRemoved(opSeq int64) {
	for i := range r.removals {
		if r.removals[i].opSeq == opSeq {
			r.removals[i].consumed = true
			return
		}
	}
}

// indexOfOpSeq returns the index of the operation carrying
// the given sequence, or -1.
func (d *Differ) indexOfOpSeq(opSeq int64) int {
	for i := range d.patch {
		if d.patch[i].opSeq == opSeq {
			return i
		}
	}
	return -1
}

// registerUnchanged indexes an equal location as a possible
// copy source.
func (r *compareRun) registerUnchanged(h uint64, node jsonNode) {
	r.unchanged[h] = append(r.unchanged[h], unchangedNode{
		node: node,
		id:   r.identityOf(node.val),
	})
}

// findUnchangedCandidate returns the pointer of an unchanged
// location whose value is deeply equal to v. Candidate
// pointers are visited in stable (lexicographic) order and,
// unlike a hash-only lookup, hash collisions can never yield
// an incorrect copy source.
func (r *compareRun) findUnchangedCandidate(d *Differ, v interface{}) string {
	h := r.digestOf(d, v, false)
	candidates := r.unchanged[h]
	if len(candidates) == 0 {
		return emptyPointer
	}
	// Stable ordering for deterministic selection.
	sorted := make([]unchangedNode, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].node.ptr < sorted[j].node.ptr
	})
	id := r.identityOf(v)
	for _, c := range sorted {
		r.stats.CopyCandidateComparisons++
		if r.equalIDs(c.node.val, v, c.id, id) {
			return c.node.ptr
		}
	}
	return emptyPointer
}
