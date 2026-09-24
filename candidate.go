package jsondiff

// candidateEntry records a removed value that may be
// recycled into a move operation, together with its
// computed digest and current position in the patch.
type candidateEntry struct {
	digest uint64
	patch  int
}

// candidateIndex is the factorization (move) candidate
// registry.
//
// Each removed value is registered exactly once, and its
// digest is computed once at registration time and reused
// for every later lookup: a candidate therefore costs a
// single digest and, at most, one recursive equality
// evaluation per actual digest collision. Buckets are kept
// in removal order, which gives the registry the same
// "first removed match wins" semantics as a linear scan of
// the patch, including for duplicate values.
//
// The patch positions are maintained as operations are
// removed or inserted, so stale entries are skipped.
// patchView gives the registry read access to the live patch.
type patchView interface {
	len() int
	op(idx int) *Operation
}

type candidateIndex struct {
	buckets map[uint64][]candidateEntry
	at      patchView
	h       *hasher
	stats   *matcherStats
}

// reset clears the registry for a new comparison.
func (c *candidateIndex) reset() {
	c.buckets = nil
}

// remove records a removed value located at patch index idx.
func (c *candidateIndex) remove(idx int, v interface{}) {
	if c.buckets == nil {
		c.buckets = make(map[uint64][]candidateEntry)
	}
	k := c.hasher().digest(v, false)
	c.buckets[k] = append(c.buckets[k], candidateEntry{
		digest: k,
		patch:  idx,
	})
}

// find returns the position in the patch of the first live
// removed value deeply equal to v, and true; or -1 and false.
// A digest mismatch excludes a candidate without any equality
// evaluation; digest agreement is always confirmed.
func (c *candidateIndex) find(v interface{}) (int, bool) {
	if c.buckets == nil {
		return -1, false
	}
	k := c.hasher().digest(v, false)
	bucket := c.buckets[k]
	for i := range bucket {
		e := &bucket[i]
		op := c.patchAt(e.patch)
		if op == nil || op.Type != OperationRemove {
			continue
		}
		if c.stats != nil {
			c.stats.CandidateChecks++
		}
		if deepEqual(op.OldValue, v) {
			return e.patch, true
		}
	}
	return -1, false
}

// patchAt returns the operation at the given position, or
// nil when the position no longer holds an operation.
func (c *candidateIndex) patchAt(idx int) *Operation {
	// The index is owned by the Differ; this indirection
	// is wired after construction.
	if c.at == nil || idx < 0 || idx >= c.at.len() {
		return nil
	}
	return c.at.op(idx)
}

// deleted keeps registered positions consistent after an
// operation has been removed from the patch at index idx.
func (c *candidateIndex) deleted(idx int) {
	for k, bucket := range c.buckets {
		out := bucket[:0]
		for _, e := range bucket {
			switch {
			case e.patch == idx:
				continue // consumed/removed entry
			case e.patch > idx:
				e.patch--
			}
			out = append(out, e)
		}
		if len(out) == 0 {
			delete(c.buckets, k)
		} else {
			c.buckets[k] = out
		}
	}
}

// inserted keeps registered positions consistent after an
// operation has been inserted into the patch at index idx.
func (c *candidateIndex) inserted(idx int) {
	for k, bucket := range c.buckets {
		for i := range bucket {
			if bucket[i].patch >= idx {
				bucket[i].patch++
			}
		}
		c.buckets[k] = bucket
	}
}

// shift adjusts candidates located in the [lo, hi) interval
// by one position, after an operation moved or landed there.
// It is used for appends, which must not shift earlier entries.
func (c *candidateIndex) shift(lo, hi int) {
	for k, bucket := range c.buckets {
		for i := range bucket {
			if bucket[i].patch >= lo && bucket[i].patch < hi {
				bucket[i].patch++
			}
		}
		c.buckets[k] = bucket
	}
}

// truncate discards every candidate whose patch position is
// at or after idx, for example when those operations are
// replaced by a rationalization.
func (c *candidateIndex) truncate(idx int) {
	for k, bucket := range c.buckets {
		out := bucket[:0]
		for _, e := range bucket {
			if e.patch < idx {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			delete(c.buckets, k)
		} else {
			c.buckets[k] = out
		}
	}
}

// hasher returns the bound hasher, or a zero-value fallback
// (the zero maphash.Hash is ready to use).
func (c *candidateIndex) hasher() *hasher {
	if c.h != nil {
		return c.h
	}
	return &hasher{}
}
