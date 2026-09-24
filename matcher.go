package jsondiff

// arrayMatcher centralizes the four responsibilities involved in
// matching two JSON arrays:
//
//   - equality: every answer for "are these two elements equal"
//     comes from deepEqual, never from a hash;
//   - hashing: digests are only used as a fast pre-filter that
//     partitions elements into candidate buckets;
//   - longest common subsequence: the LCS dynamic-programming
//     table and its backtracking are computed on top of the
//     equality matrix built once below;
//   - move/copy discovery: the factorize pass reuses the same
//     matrix instead of re-comparing removed values against new
//     elements.
//
// Each array pair creates exactly one matcher, so every (old,new)
// candidate pair is accounted at most once. The counters exposed
// on the parent Differ are diagnostics only; the matching result
// never depends on them.
type arrayMatcher struct {
	d   *Differ
	src []interface{}
	tgt []interface{}

	// srcHash/tgtHash hold the digest of each element, computed
	// once for the whole comparison.
	srcHash []uint64
	tgtHash []uint64

	// equal is the candidate matrix: rows index src elements,
	// columns tgt elements. A value of 0 means "not probed yet",
	// 1 "proven equal" and 2 "proven different". It allows the
	// LCS table, its backtracking and move discovery to share a
	// single equality answer per pair.
	equal []byte

	// removedSrc records, in emission order, the source indices
	// removed while traversing the LCS. It lets move/copy
	// discovery locate the remove operation emitted for a given
	// source element without re-running element comparisons.
	removedSrc []int

	// snapshotPatchLen is the patch length captured before the
	// LCS traversal starts emitting operations; intra-array move
	// operations are inserted at that position, preserving the
	// historical output order.
	snapshotPatchLen int
}

const (
	candidateUnknown     = 0
	candidateEqual       = 1
	candidateDifferent   = 2
)

func newArrayMatcher(d *Differ, src, tgt []interface{}) *arrayMatcher {
	m := &arrayMatcher{
		d:                d,
		src:              src,
		tgt:              tgt,
		srcHash:          make([]uint64, len(src)),
		tgtHash:          make([]uint64, len(tgt)),
		equal:            make([]byte, len(src)*len(tgt)),
		snapshotPatchLen: len(d.patch),
	}
	m.build()
	return m
}

// build digests every element exactly once and accounts every
// candidate pair exactly once. Hash collisions are resolved with
// deepEqual, so duplicate elements can never be paired on the
// basis of a digest alone.
func (m *arrayMatcher) build() {
	for i, v := range m.src {
		m.srcHash[i] = m.d.elementDigest(v)
	}
	for j, v := range m.tgt {
		m.tgtHash[j] = m.d.elementDigest(v)
	}

	d := m.d
	for i := 0; i < len(m.src); i++ {
		row := i * len(m.tgt)
		for j := 0; j < len(m.tgt); j++ {
			d.stats.candidateProbes++
			if m.srcHash[i] != m.tgtHash[j] {
				m.equal[row+j] = candidateDifferent
				continue
			}
			d.stats.hashCollisions++
			d.stats.candidateComparisons++
			if deepEqual(m.src[i], m.tgt[j]) {
				m.equal[row+j] = candidateEqual
			} else {
				m.equal[row+j] = candidateDifferent
			}
		}
	}

	// Test-only fault seam: it runs only when armed by the
	// internal diagnostic hooks and never in production code.
	// It is invoked after the candidate matrix has been fully
	// accounted but before any patch operation is emitted, so a
	// failure cannot leave a half-updated public patch.
	if d.fault != nil {
		d.fault(faultStageArrayCandidates, m.src, m.tgt)
	}
}

// candidateEqual answers a single equality question and records
// the answer in the shared candidate matrix.
func (m *arrayMatcher) candidateEqual(i, j int) bool {
	return m.equal[i*len(m.tgt)+j] == candidateEqual
}

// lcs computes the longest common subsequence on top of the
// candidate matrix and returns the matched index pairs.
func (m *arrayMatcher) lcs() [][2]int {
	ns, nt := len(m.src), len(m.tgt)
	table := make([][]int, ns+1)
	for i := 0; i <= ns; i++ {
		table[i] = make([]int, nt+1)
	}
	for i := 1; i <= ns; i++ {
		row := table[i]
		prev := table[i-1]
		for j := 1; j <= nt; j++ {
			if m.candidateEqual(i-1, j-1) {
				row[j] = prev[j-1] + 1
			} else if prev[j] > row[j-1] {
				row[j] = prev[j]
			} else {
				row[j] = row[j-1]
			}
		}
	}

	i, j := ns, nt
	pairs := make([][2]int, 0, table[i][j])
	for i > 0 && j > 0 {
		switch {
		case m.candidateEqual(i-1, j-1):
			m.d.stats.lcsBacktracks++
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

// removeCandidate marks the source element removed at index i as
// a potential move source. Indices are recorded in emission
// order.
func (m *arrayMatcher) removeCandidate(i int) {
	m.removedSrc = append(m.removedSrc, i)
}

// findRemoved returns the patch index of an unconsumed remove
// operation emitted by this array whose old value is deeply
// equal to v, or -1 when none exists. Equality answers are read
// from the candidate matrix instead of re-computing comparisons;
// the k-th remaining remove located inside the LCS snapshot
// window corresponds to the k-th removed source index.
func (m *arrayMatcher) findRemoved(targetPath string, j int) int {
	consumed := 0
	for k, i := range m.removedSrc {
		// Skip source indices already consumed by a previous
		// move, exactly as the historical linear scan does.
		if m.equal[i*len(m.tgt)+j] != candidateEqual {
			continue
		}
		pos := m.locateRemove(k, &consumed)
		if pos == -1 {
			continue
		}
		// A location cannot be moved into one of its children.
		if hasProperPathPrefix(m.d.patch[pos].Path, targetPath) {
			return -1
		}
		m.removedSrc = removeIndex(m.removedSrc, k)
		return pos
	}
	return -1
}

// locateRemove maps the rank of a still-unconsumed removed
// source index to the position of its remove operation inside
// the snapshot window of the patch.
func (m *arrayMatcher) locateRemove(k int, consumed *int) int {
	rank := k - *consumed
	seen := 0
	for p := m.snapshotPatchLen; p < len(m.d.patch); p++ {
		if m.d.patch[p].Type != OperationRemove {
			continue
		}
		if seen == rank {
			*consumed++
			return p
		}
		seen++
	}
	return -1
}

func removeIndex(s []int, i int) []int {
	return append(s[:i], s[i+1:]...)
}
