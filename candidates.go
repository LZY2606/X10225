package jsondiff

// This file implements the single ledger used to account for
// move and copy candidates. Every remove operation emitted by
// the production diff path is recorded exactly once; when an
// add operation is emitted, the ledger resolves a candidate
// at most once per (candidate, target) pair and materializes
// a move when both values are deeply equal.

// moveCandidate records a remove operation that can later be
// paired with an add to form a move.
type moveCandidate struct {
	// patchIdx is the index occupied by the remove
	// operation in the current patch.
	patchIdx int
	// digest of the removed value, computed once.
	digest uint64
	// path and value of the remove operation.
	path string
	val  interface{}
	// srcIdx is the source-array index of the removed
	// element when the ledger entry originates from an LCS
	// array comparison; -1 otherwise. It lets the emitter
	// account for elements hoisted out as moves when it
	// computes the in-place paths of later operations.
	srcIdx int
	// used reports that the candidate materialized a
	// move and the remove operation is gone.
	used bool
	// gone reports that the operation was rewritten
	// (rationalization) before being consumed.
	gone bool
}

// recordRemove appends the remove operation to the move
// ledger. The digest is computed once and reused by every
// later lookup.
func (d *Differ) recordRemove(patchIdx int, path string, v interface{}, srcIdx int) {
	d.moveLedger = append(d.moveLedger, moveCandidate{
		patchIdx: patchIdx,
		digest:   d.digestValue(v, false),
		path:     path,
		val:      v,
		srcIdx:   srcIdx,
	})
}

// digestValue computes the structural digest of v. The
// optional digest override exists solely to let the internal
// test suite force digest collisions; it is never wired to a
// public option and the production equality checks remain
// based on deep equality.
func (d *Differ) digestValue(v interface{}, sorted bool) uint64 {
	if d.digestOverride != nil {
		return d.digestOverride(v)
	}
	return d.hasher.digest(v, sorted)
}

// findMoveCandidate looks for the first live remove operation
// whose digest matches and whose value is deeply equal to v.
// The digest only narrows candidates: duplicate values and
// digest collisions are resolved by one deep equality probe.
// It returns the candidate index in the ledger, or -1.
func (d *Differ) findMoveCandidate(v interface{}, vdigest uint64) int {
	for i := range d.moveLedger {
		c := &d.moveLedger[i]
		if c.used || c.gone {
			continue
		}
		if c.digest != vdigest {
			continue
		}
		if d.probeMoveEqual(c, v) {
			return i
		}
	}
	return -1
}

// probeMoveEqual performs the single deep equality evaluation
// between a move candidate and an added value, counting it in
// the diagnostics.
func (d *Differ) probeMoveEqual(c *moveCandidate, v interface{}) bool {
	if d.matchStats != nil {
		d.matchStats.moveCandidates++
		d.matchStats.deepEqualProbes++
	}
	return deepEqual(c.val, v)
}

// consumeMove marks the candidate as used and drops its
// remove operation from the patch, adjusting the indices of
// the remaining candidates. It returns the candidate path.
func (d *Differ) consumeMove(ledgerIdx int) string {
	c := &d.moveLedger[ledgerIdx]
	c.used = true
	from := c.path
	removed := c.patchIdx

	d.patch = d.patch.remove(removed)
	for i := range d.moveLedger {
		cc := &d.moveLedger[i]
		if cc.gone || cc.used {
			continue
		}
		if cc.patchIdx > removed {
			cc.patchIdx--
		}
	}
	if d.matchStats != nil {
		d.matchStats.moveFound++
	}
	return from
}

// dropLedgerTail invalidates ledger entries whose operations
// were rewritten after a rationalization replaced the tail of
// the patch.
func (d *Differ) dropLedgerTail(patchLen int) {
	kept := d.moveLedger[:0]
	for _, c := range d.moveLedger {
		if c.used {
			continue
		}
		if c.patchIdx >= patchLen {
			continue
		}
		kept = append(kept, c)
	}
	d.moveLedger = kept
}

// unorderedDeepEqualSlice reports whether the two arrays
// contain the same multiset of elements regardless of
// ordering. It delegates to the shared arrayMatcher, which
// is also the production implementation used by the array
// diff paths.
func (d *Differ) unorderedDeepEqualSlice(src, tgt []interface{}) bool {
	return newArrayMatcher(d, src, tgt, nil).unorderedEqual()
}
