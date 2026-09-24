package jsondiff

// moveCandidate represents an element removed from the
// source document that may be reused by a subsequent add
// operation instead of emitting both a remove and an add.
//
// A candidate is discovered exactly once, when the remove
// operation is emitted. Later add operations resolve
// candidates through the unified [candidateIndex], which
// groups them by digest and always verifies equality
// before pairing, so that duplicate values or digest
// collisions can never cause an incorrect pairing.
type moveCandidate struct {
	// value is the element removed from the source.
	value interface{}
	// digest is the precomputed digest of value.
	digest uint64
	// srcIdx is the index of value in the source array
	// when the candidate was produced while matching an
	// array with LCS, or -1 for other candidates. It
	// allows move discovery to reuse the equality results
	// of the array matcher instead of comparing values a
	// second time.
	srcIdx int
	// opIdx is the live index of the remove operation in
	// the patch being generated. It is kept up-to-date by
	// the candidate index when operations that precede it
	// are inserted or removed.
	opIdx int
	used  bool
}

// candidateIndex is the single place responsible for
// move/copy candidate discovery.
//
// Candidates are grouped by digest, which acts as a fast
// negative filter: a lookup never compares values whose
// digest differs. Values that share a digest are always
// verified with deep equality, therefore duplicate
// elements are paired by multiplicity and digest
// collisions cannot produce false matches.
type candidateIndex struct {
	byHash map[uint64][]*moveCandidate
	stats  *compareStats
}

func (ci *candidateIndex) reset(stats *compareStats) {
	ci.byHash = nil
	ci.stats = stats
}

// register records a freshly emitted remove operation as
// a move candidate. srcIdx must be the source-array index
// of value when matching with LCS, or -1 otherwise.
func (ci *candidateIndex) register(opIdx, srcIdx int, h uint64, v interface{}) {
	if ci.byHash == nil {
		ci.byHash = make(map[uint64][]*moveCandidate)
	}
	ci.byHash[h] = append(ci.byHash[h], &moveCandidate{
		value:   v,
		digest:  h,
		srcIdx: srcIdx,
		opIdx:   opIdx,
	})
}

// find returns the first unused candidate equal to v, or
// nil when none matches. When m is the array matcher used
// to produce the candidates and tgtIdx is the index of v
// in the target array, equality is delegated to the
// matcher, whose memo guarantees that any given pair of
// elements is compared at most once for both LCS and move
// discovery. Every lookup increments the diagnostic
// candidate-lookup counter exactly once.
func (ci *candidateIndex) find(m *arrayMatcher, tgtIdx int, h uint64, v interface{}) *moveCandidate {
	if ci.stats != nil {
		ci.stats.candidateLookups++
	}
	for _, c := range ci.byHash[h] {
		if c.used {
			continue
		}
		eq := false
		switch {
		case m != nil && c.srcIdx >= 0:
			eq = m.equal(c.srcIdx, tgtIdx)
		default:
			eq = deepEqual(c.value, v)
		}
		if eq {
			return c
		}
	}
	return nil
}

// consume marks c as used and adjusts the live indices of
// the remaining candidates for a remove at rmIdx followed
// by an insert at insIdx. When insIdx is negative, only
// the removal is accounted for.
func (ci *candidateIndex) consume(c *moveCandidate, rmIdx, insIdx int) {
	c.used = true
	for _, bucket := range ci.byHash {
		for _, other := range bucket {
			if other.used {
				continue
			}
			if other.opIdx > rmIdx {
				other.opIdx--
			}
			if insIdx >= 0 && other.opIdx >= insIdx {
				other.opIdx++
			}
		}
	}
}
