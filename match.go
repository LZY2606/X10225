package jsondiff

import (
	"encoding/json"
	"errors"
	"reflect"
)

// ErrArrayMatchAmbiguous is the sentinel error returned when the
// array matcher cannot decide whether two elements are equal.
// The production comparison paths never produce an ambiguous
// verdict; it is only surfaced when an explicit fault is injected
// (diagnostics/testing) to guarantee that an unknown result is
// never guessed as a success.
// A failed comparison leaves no partial patch, cache or counter
// state behind, and retrying it with the same input is safe and
// deterministic.
var ErrArrayMatchAmbiguous = errors.New("jsondiff: array matching result is ambiguous")

// matchStats holds diagnostic counters produced while comparing
// arrays. Counters are diagnostics only: they are never consulted
// to build a patch and as such cannot influence its content.
// Every Differ owns a private instance, so counters observed by
// concurrent comparisons are isolated.
type matchStats struct {
	// PairProbes is the number of old/new element pairs for
	// which an equality verdict was requested.
	PairProbes int
	// DeepCompares is the number of pairs that passed the
	// digest pre-filter and were compared structurally. Each
	// pair is deep-compared at most once.
	DeepCompares int
	// HashDigests is the number of element digests that were
	// computed. A digest is computed at most once per element.
	HashDigests int
	// MoveCandidates is the number of removed values examined
	// while searching for a move candidate.
	MoveCandidates int
	// CopyCandidates is the number of unchanged locations
	// examined while searching for a copy candidate.
	CopyCandidates int
	// Ambiguous is the number of comparisons that were
	// refused because their result could not be determined.
	Ambiguous int
}

// arrayMatcher centralizes, for a single pair of arrays, the four
// responsibilities involved in array diffing:
//   - equality: structural comparison of elements (ordered or
//     unordered/multiset),
//   - hashing: per-element digests used as a collision-prone
//     pre-filter, never as a source of truth,
//   - LCS: the longest common subsequence built from memoized
//     equality verdicts,
//   - move/copy candidate discovery: verdict reuse when a
//     factorized add searches for a previously removed element.
//
// A matcher is created per compared array and never shared
// between comparisons; each (i,j) pair is probed at most once and
// each element digest is computed at most once.
type arrayMatcher struct {
	d           *Differ
	src         []interface{}
	tgt         []interface{}
	unordered   bool
	srcHashes   []uint64
	tgtHashes   []uint64
	srcDigested []bool
	tgtDigested []bool
	verdicts    map[uint64]bool
}

func newArrayMatcher(d *Differ, src, tgt []interface{}, unordered bool) *arrayMatcher {
	return &arrayMatcher{
		d:           d,
		src:         src,
		tgt:         tgt,
		unordered:   unordered,
		srcHashes:   make([]uint64, len(src)),
		tgtHashes:   make([]uint64, len(tgt)),
		srcDigested: make([]bool, len(src)),
		tgtDigested: make([]bool, len(tgt)),
		verdicts:    make(map[uint64]bool),
	}
}

// verdict returns whether src[i] and tgt[j] are equal. The result
// is memoized, so the structural comparison of a given pair is
// evaluated at most once.
func (m *arrayMatcher) verdict(i, j int) bool {
	m.d.stats.PairProbes++

	key := uint64(i)*uint64(len(m.tgt)+1) + uint64(j)
	if eq, ok := m.verdicts[key]; ok {
		return eq
	}
	eq := m.probe(i, j)
	m.verdicts[key] = eq
	return eq
}

// probe applies the digest pre-filter before falling back to a
// structural comparison. Digests are only used to reject
// unequal pairs: equal digests are always confirmed structurally,
// which makes a digest collision unable to pair distinct elements.
func (m *arrayMatcher) probe(i, j int) bool {
	if m.srcHash(i) != m.tgtHash(j) {
		return false
	}
	m.d.stats.DeepCompares++

	// An injected failure is a recoverable, explicit "unknown":
	// refuse to decide rather than guessing equality.
	if m.d.probeFault != nil && m.d.probeFault() {
		m.d.stats.Ambiguous++
		panic(ErrArrayMatchAmbiguous)
	}
	if m.unordered {
		return unorderedDeepEqual(m.src[i], m.tgt[j])
	}
	return deepEqual(m.src[i], m.tgt[j])
}

func (m *arrayMatcher) srcHash(i int) uint64 {
	if !m.srcDigested[i] {
		m.srcHashes[i] = m.d.digest(m.src[i], m.unordered)
		m.srcDigested[i] = true
		m.d.stats.HashDigests++
	}
	return m.srcHashes[i]
}

func (m *arrayMatcher) tgtHash(j int) uint64 {
	if !m.tgtDigested[j] {
		m.tgtHashes[j] = m.d.digest(m.tgt[j], m.unordered)
		m.tgtDigested[j] = true
		m.d.stats.HashDigests++
	}
	return m.tgtHashes[j]
}

// lcs computes the longest common subsequence and returns the
// index pairs of matching elements, using the same DP and
// backtracking approach as the historical implementation but
// backed by memoized verdicts.
func (m *arrayMatcher) lcs() [][2]int {
	n, p := len(m.src), len(m.tgt)
	t := make([][]int, n+1)
	for i := 0; i <= n; i++ {
		t[i] = make([]int, p+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if m.verdict(i-1, j-1) {
				t[i][j] = t[i-1][j-1] + 1
			} else {
				t[i][j] = max(t[i-1][j], t[i][j-1])
			}
		}
	}
	i, j := n, p
	s := make([][2]int, 0, t[i][j])

	for i > 0 && j > 0 {
		switch {
		case m.verdict(i-1, j-1):
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

// multisetEqual reports whether both arrays contain the same
// elements regardless of their order. Matching is one-to-one and
// confirmed structurally, so duplicate elements are counted
// correctly and digest collisions cannot create false matches.
func (m *arrayMatcher) multisetEqual() bool {
	if len(m.src) != len(m.tgt) {
		return false
	}
	used := make([]bool, len(m.tgt))
	for i := 0; i < len(m.src); i++ {
		hi := m.srcHash(i)
		matched := -1
		for j := 0; j < len(m.tgt); j++ {
			if !used[j] && hi == m.tgtHash(j) && m.verdict(i, j) {
				matched = j
				break
			}
		}
		if matched == -1 {
			return false
		}
		used[matched] = true
	}
	return true
}

// verdictForValues reuses an already evaluated verdict when old and
// new are elements of the arrays matched by this matcher. The
// boolean result is valid only when the second return value is
// true; callers fall back to a structural comparison otherwise.
// Composite values are identified by reference and scalars by
// value, so a nested value can never borrow an unrelated verdict.
func (m *arrayMatcher) verdictForValues(old, new interface{}) (bool, bool) {
	i := indexOf(m.src, old)
	j := indexOf(m.tgt, new)
	if i == -1 || j == -1 {
		return false, false
	}
	return m.verdict(i, j), true
}

// indexOf returns the position of v in a. Composite JSON values
// are located by reference, scalar values by equality.
func indexOf(a []interface{}, v interface{}) int {
	switch v.(type) {
	case []interface{}, map[string]interface{}:
		p := reflect.ValueOf(v).Pointer()
		for i, e := range a {
			if reflect.ValueOf(e).Pointer() == p {
				return i
			}
		}
	default:
		t := jsonTypeSwitch(v)
		for i, e := range a {
			if jsonTypeSwitch(e) == t && scalarEqual(e, v) {
				return i
			}
		}
	}
	return -1
}

func scalarEqual(a, b interface{}) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case string:
		v, ok := b.(string)
		return ok && a == v
	case bool:
		v, ok := b.(bool)
		return ok && a == v
	case float64:
		v, ok := b.(float64)
		return ok && a == v
	case json.Number:
		v, ok := b.(json.Number)
		return ok && a == v
	default:
		return false
	}
}

// unorderedDeepEqual compares two JSON values treating nested
// arrays as unordered multisets. Objects are compared as in
// deepEqual but their values are compared recursively.
func unorderedDeepEqual(src, tgt interface{}) bool {
	st := jsonTypeSwitch(src)
	if st != jsonTypeSwitch(tgt) {
		return false
	}
	switch st {
	case jsonArray:
		return unorderedMultisetEqual(src.([]interface{}), tgt.([]interface{}))
	case jsonObject:
		oobj := src.(map[string]interface{})
		nobj := tgt.(map[string]interface{})
		if len(oobj) != len(nobj) {
			return false
		}
		for k, v1 := range oobj {
			v2, ok := nobj[k]
			if !ok || !unorderedDeepEqual(v1, v2) {
				return false
			}
		}
		return true
	default:
		return deepEqual(src, tgt)
	}
}

func unorderedMultisetEqual(src, tgt []interface{}) bool {
	if len(src) != len(tgt) {
		return false
	}
	used := make([]bool, len(tgt))
	for _, a := range src {
		matched := -1
		for j, b := range tgt {
			if !used[j] && unorderedDeepEqual(a, b) {
				matched = j
				break
			}
		}
		if matched == -1 {
			return false
		}
		used[matched] = true
	}
	return true
}
