package jsondiff

// arrayMatcher is the single implementation used by every
// array comparison path:
//
//   - the LCS-based comparison (LCS option),
//   - the unordered/equivalence short-circuit (Equivalent
//     option), and
//   - the standalone lcs helper exercised by the unit tests.
//
// It never owns comparison logic of its own: every equality
// decision goes through the run-wide memoized predicate, and
// every digest goes through the run-wide digest cache. As a
// consequence, a candidate pair of values is computed at
// most once for the whole comparison, regardless of how many
// arrays contain it.
type arrayMatcher struct {
	d         *Differ
	r         *compareRun
	src       []interface{}
	tgt       []interface{}
	sortHash  bool
	useHash   bool
	lcsMode   bool
}

// newArrayMatcher builds a matcher for the two arrays.
// sortHash enables the unordered digest (nested arrays are
// hashed in sorted order), and useHash enables the digest
// prefilter in front of deep equality.
func (d *Differ) newArrayMatcher(src, tgt []interface{}, sortHash, useHash bool) *arrayMatcher {
	if d.run == nil {
		d.beginRun()
	}
	return &arrayMatcher{
		d:        d,
		r:        d.run,
		src:      src,
		tgt:      tgt,
		sortHash: sortHash,
		useHash:  useHash,
	}
}

// hashMismatch reports whether the digests of src[i] and
// tgt[j] provably differ. Equal values always share a digest,
// so a mismatch is a sound short-circuit; equal digests still
// have to be confirmed with deep equality.
func (m *arrayMatcher) hashMismatch(i, j int) bool {
	hs := m.r.digestOf(m.d, m.src[i], m.sortHash)
	ht := m.r.digestOf(m.d, m.tgt[j], m.sortHash)
	return hs != ht
}

// equalAt is the single equality oracle used by the LCS
// matrix and the multiset matching.
func (m *arrayMatcher) equalAt(i, j int) bool {
	if m.lcsMode {
		m.r.stats.LCSProbes++
	}
	if m.useHash && m.hashMismatch(i, j) {
		return false
	}
	return m.r.equal(m.src[i], m.tgt[j])
}

// lcsPairs computes the index pairs of the longest common
// subsequence between the source and target arrays. Each
// matrix cell is consulted exactly once; the backtracking
// pass reads the computed table instead of comparing values
// again.
func (m *arrayMatcher) lcsPairs() [][2]int {
	src, tgt := m.src, m.tgt
	m.lcsMode = true
	tab := make([][]int, len(src)+1)
	for i := 0; i <= len(src); i++ {
		tab[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(tab); i++ {
		for j := 1; j < len(tab[i]); j++ {
			if m.equalAt(i-1, j-1) {
				tab[i][j] = tab[i-1][j-1] + 1
			} else {
				tab[i][j] = max(tab[i-1][j], tab[i][j-1])
			}
		}
	}
	i, j := len(src), len(tgt)
	s := make([][2]int, 0, tab[i][j])
	for i > 0 && j > 0 {
		switch {
		// The result was already computed by equalAt for
		// this cell; the memo serves it without spending a
		// new comparison.
		case m.memoEqual(i-1, j-1):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case tab[i-1][j] > tab[i][j-1]:
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

// memoEqual reports equality for values compared earlier
// during the matrix fill. It never triggers a new comparison:
// values whose digests differed during the fill are not
// present as "equal" in the memo, which keeps the
// backtracking decision identical to the legacy one that
// re-ran deepEqual at every step.
func (m *arrayMatcher) memoEqual(i, j int) bool {
	ia := m.r.identityOf(m.src[i])
	ib := m.r.identityOf(m.tgt[j])
	if ia == ib {
		return true
	}
	state, ok := m.r.eq[normalizeKey(ia, ib)]
	return ok && state == eqEqual
}

// multisetEqual reports whether the two arrays contain the
// same values up to ordering, honoring multiplicities. Each
// target occurrence is matched at most once, and duplicate
// values therefore cannot be paired with the same element.
// Digests are used only as a prefilter; every candidate is
// confirmed with deep equality.
func (m *arrayMatcher) multisetEqual() bool {
	src, tgt := m.src, m.tgt
	if len(src) != len(tgt) {
		return false
	}
	matched := make([]bool, len(tgt))
	for i := 0; i < len(src); i++ {
		matchedOnce := false
		for j := 0; j < len(tgt); j++ {
			if matched[j] {
				continue
			}
			if m.equalAt(i, j) {
				matched[j] = true
				matchedOnce = true
				break
			}
		}
		if !matchedOnce {
			return false
		}
	}
	return true
}
