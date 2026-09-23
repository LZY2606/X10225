package jsondiff

// ComparisonStats contains diagnostic counters that quantify
// the work performed by the array matching and the move/copy
// candidate discovery of a Differ during a comparison.
//
// The counters are strictly diagnostic: they have no effect
// on the generated patch. They are stored per Differ instance
// and reset at the beginning of each comparison, so concurrent
// comparisons performed by distinct Differ instances are fully
// isolated from each other.
type ComparisonStats struct {
	// Equalities is the number of deep-equality comparisons
	// performed while matching the elements of arrays (LCS)
	// and while verifying move/copy candidates.
	Equalities uint64

	// HashCollisions is the number of times a move/copy
	// candidate selected by its hash failed the subsequent
	// deep-equality verification. Hash matches alone never
	// decide a pairing; equality always does.
	HashCollisions uint64
}

// equal reports whether src and tgt are deeply equal, and
// records the comparison in the diagnostic counters of the
// Differ. It is the single equality decision point shared by
// the LCS array matching and the move/copy candidate
// verification.
func (d *Differ) equal(src, tgt interface{}) bool {
	d.stats.Equalities++
	return deepEqual(src, tgt)
}

// noteCollision records that a candidate selected by its hash
// did not pass the deep-equality verification.
func (d *Differ) noteCollision() {
	d.stats.HashCollisions++
}

// digest returns the hash of the given value. The digestFn
// field of the Differ, when set, replaces the default hasher;
// it exists so that tests can exercise the collision handling
// of the matching logic, which is the production code path
// regardless of the hash function in use.
func (d *Differ) digest(v interface{}, sort bool) uint64 {
	if d.digestFn != nil {
		return d.digestFn(v, sort)
	}
	return d.hasher.digest(v, sort)
}

// indexRemoveCandidate records the position of a remove
// operation in the patch, indexed by the hash of the removed
// value, so that a subsequent addition of an equal value can
// be matched with it in constant time to form a move operation.
// Each removed value is hashed exactly once, when its remove
// operation is generated.
func (d *Differ) indexRemoveCandidate(idx int, v interface{}) {
	if d.candidates == nil {
		d.candidates = make(map[uint64][]int)
	}
	k := d.digest(v, false)
	d.candidates[k] = append(d.candidates[k], idx)
}

// findRemoved returns the index of the first remove operation
// of the patch whose old value is deeply equal to v, or -1 if
// there is none. Candidates are selected by their hash and
// confirmed by a deep-equality comparison, so distinct values
// that collide under the hash function are never paired.
func (d *Differ) findRemoved(v interface{}) int {
	if d.candidates == nil {
		return -1
	}
	k := d.digest(v, false)
	for _, idx := range d.candidates[k] {
		if d.equal(d.patch[idx].OldValue, v) {
			return idx
		}
		d.noteCollision()
	}
	return -1
}

// removeOp removes the operation at index idx from the patch
// and updates the move candidates index accordingly.
func (d *Differ) removeOp(idx int) {
	d.patch = d.patch.remove(idx)
	if d.candidates == nil {
		return
	}
	for k, idxs := range d.candidates {
		kept := idxs[:0]
		for _, i := range idxs {
			switch {
			case i == idx:
				// The candidate has been consumed.
			case i > idx:
				kept = append(kept, i-1)
			default:
				kept = append(kept, i)
			}
		}
		if len(kept) == 0 {
			delete(d.candidates, k)
		} else {
			d.candidates[k] = kept
		}
	}
}

// insertOp inserts a new operation in the patch at position
// pos and updates the move candidates index accordingly.
func (d *Differ) insertOp(pos int, typ, from, path string, src, tgt interface{}, vl int) {
	d.patch = d.patch.insert(pos, typ, from, path, src, tgt, vl)
	if d.candidates == nil {
		return
	}
	for _, idxs := range d.candidates {
		for i, idx := range idxs {
			if idx >= pos {
				idxs[i] = idx + 1
			}
		}
	}
}

// truncatePatch shrinks the patch to its first n operations
// and drops the move candidates that referred to the removed
// tail.
func (d *Differ) truncatePatch(n int) {
	d.patch = d.patch[:n]
	if d.candidates == nil {
		return
	}
	for k, idxs := range d.candidates {
		kept := idxs[:0]
		for _, i := range idxs {
			if i < n {
				kept = append(kept, i)
			}
		}
		if len(kept) == 0 {
			delete(d.candidates, k)
		} else {
			d.candidates[k] = kept
		}
	}
}
