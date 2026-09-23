package jsondiff

// MatcherStats holds diagnostic counters collected during a
// single Compare invocation. The counters never influence the
// generated patch: they exist solely to make the amount of work
// performed by array matching observable and quantifiable.
//
// Field order is part of the stable diagnostic output.
type MatcherStats struct {
	// EqualDeep is the number of deepEqual calls performed,
	// within array matchers. A (src,tgt) pair is settled at
	// most once per matcher.
	EqualDeep int
	// EqualFallback is the number of deepEqual calls performed
	// outside of an array matcher (cross-array/object move
	// discovery); these cannot share a pair table.
	EqualFallback int
	// HashDigest is the number of value digest computations
	// performed across hashing, equivalence, factorization
	// preparation and copy candidate discovery.
	HashDigest int
	// Matchers is the number of per-array matchers created.
	Matchers int
	// MoveCandidates is the number of remove/add pairs that
	// were consolidated into a move operation.
	MoveCandidates int
	// CopyCandidates is the number of add operations that
	// were served as copy operations from unchanged nodes.
	CopyCandidates int
}

// removalEntry is a remove operation registered once as a
// potential move source. Entries are ordered by emission.
type removalEntry struct {
	id     int
	path   string
	val    interface{}
	m      *arrayMatcher // matcher owning the value, if any
	srcIdx int           // source index within m.src, if any
	used   bool
}

// removalLedger is the single registry of move candidates for
// one comparison. Every removed value is recorded once and can
// be consumed at most once, regardless of which array or object
// produced it.
type removalLedger struct {
	entries []*removalEntry
}

// moveProbe identifies the add side of a potential move.
type moveProbe struct {
	m       *arrayMatcher // array being emitted, nil for objects
	tgtIdx  int           // target index within m.tgt
	lcs     bool          // insertion order follows the LCS snapshot
	liveIdx int           // current live index of the target slot (LCS)
}

func (d *Differ) recordRemoval(path string, v interface{}, m *arrayMatcher, srcIdx int) int {
	if !d.opts.factorize {
		return 0
	}
	// Warm the per-element digest so the value is hashed at
	// most once; equality, not the digest, drives matching.
	if m != nil {
		m.digestSrc(srcIdx)
	} else {
		d.digest(v, false)
	}
	e := &removalEntry{
		id:     len(d.removals.entries) + 1,
		path:   path,
		val:    v,
		m:      m,
		srcIdx: srcIdx,
	}
	d.removals.entries = append(d.removals.entries, e)
	return e.id
}

// entriesEqual settles equality between a registered removal and
// a probed addition, reusing the memoized pair table when both
// sides belong to the same array matcher.
func (d *Differ) entriesEqual(e *removalEntry, p moveProbe, v interface{}) bool {
	if e.m != nil && p.m != nil && e.m == p.m {
		return e.m.equal(e.srcIdx, p.tgtIdx)
	}
	d.stats.EqualFallback++
	return deepEqual(e.val, v)
}

// takeMove finds the earliest unused removal equal to v. On
// success it returns the registered entry together with the
// current index of its remove operation within the patch. A
// prefix-conflicting candidate reproduces the historical
// behavior of suppressing the addition, reported with ok=true
// and patchIdx=-1.
func (d *Differ) takeMove(path string, v interface{}, p moveProbe) (e *removalEntry, patchIdx int, ok bool) {
	for _, e := range d.removals.entries {
		if e.used {
			continue
		}
		if !d.entriesEqual(e, p, v) {
			continue
		}
		if hasProperPathPrefix(e.path, path) {
			return e, -1, true
		}
		e.used = true
		d.stats.MoveCandidates++

		idx := -1
		for i := range d.patch {
			if d.patch[i].candID == e.id {
				idx = i
				break
			}
		}
		return e, idx, true
	}
	return nil, 0, false
}

// findCopy returns the pointer of an unchanged node whose value
// is deeply equal to v, or emptyPointer. The hash lookup is only
// a prefilter: deep equality is always verified, so a digest
// collision cannot select the wrong source.
func (d *Differ) findCopy(v interface{}, p moveProbe) string {
	if d.hashmap == nil {
		return emptyPointer
	}
	var k uint64
	if p.m != nil {
		k = p.m.digestTgt(p.tgtIdx)
	} else {
		k = d.digest(v, false)
	}
	node, ok := d.hashmap[k]
	if !ok {
		return emptyPointer
	}
	d.stats.EqualFallback++
	if !deepEqual(node.val, v) {
		return emptyPointer
	}
	d.stats.CopyCandidates++
	return node.ptr
}

// MatcherStats returns a copy of the diagnostic counters
// accumulated during the most recent comparison.
func (d *Differ) MatcherStats() MatcherStats {
	return MatcherStats(d.stats)
}
