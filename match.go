package jsondiff

// diffStats holds diagnostic counters collected while a
// patch is generated. The counters are reset by Reset and
// are always isolated per Differ instance; they are meant
// for diagnostic purposes only and never influence the
// content of the generated patch.
type diffStats struct {
	// comparisons counts the deep-equality comparisons
	// performed to confirm the equality of values whose
	// hash digest matched while pairing the elements of
	// arrays (LCS) and discovering move/copy candidates.
	comparisons uint64
}

// A matcher unifies the equality and hashing concerns of
// the array comparison. The hash digest of each element is
// computed once and reused as a pre-filter, so that the more
// expensive deep-equality comparison only runs to confirm a
// digest match. This guarantees that distinct values that
// happen to collide on the same digest are never paired
// together by mistake.
//
// The matcher shares the hasher of the Differ it originates
// from, and accounts comparisons in the latter's diagnostic
// counters.
type matcher struct {
	hasher *hasher
	stats  *diffStats
}

// digests returns the hash digests of the given values, in
// the same order. The digest of each value is computed once.
func (m *matcher) digests(vals []interface{}) []uint64 {
	digests := make([]uint64, len(vals))
	for i, v := range vals {
		digests[i] = m.hasher.digest(v, false)
	}
	return digests
}

// equal reports whether the values src and tgt are deeply
// equal. When their digests differ, the values are known to
// be different and the deep comparison is skipped, unless one
// of the values has an invalid JSON type, in which case the
// comparison always runs so that unsupported inputs keep
// surfacing through the usual error path instead of being
// silently treated as different.
func (m *matcher) equal(src, tgt interface{}, srcDigest, tgtDigest uint64) bool {
	if srcDigest != tgtDigest &&
		jsonTypeSwitch(src) != jsonInvalid &&
		jsonTypeSwitch(tgt) != jsonInvalid {
		return false
	}
	m.stats.comparisons++
	return deepEqual(src, tgt)
}

// lcs computes the longest common subsequence of the two
// slices using the matcher to pair equal elements, and
// returns the index pairs of the LCS, i.e. the indices
// into src and tgt where the matched items are located.
//
// The structure of the returned pairs is identical to that
// of the direct deep-equality implementation: the digest is
// only a pre-filter and every candidate pair is confirmed
// by deepEqual before being accepted.
func (m *matcher) lcs(src, tgt []interface{}) [][2]int {
	srcDigests := m.digests(src)
	tgtDigests := m.digests(tgt)

	eq := func(i, j int) bool {
		return m.equal(src[i], tgt[j], srcDigests[i], tgtDigests[j])
	}

	t := make([][]int, len(src)+1)
	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if eq(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := len(src), len(tgt)
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case eq(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t[i-1][j] > t[i][j-1]:
			i--
		default:
			j--
		}
	}
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}
