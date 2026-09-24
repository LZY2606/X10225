package jsondiff

import "reflect"

// Stats holds diagnostic counters collected while a Differ
// generates a patch. The counters are purely informational;
// they never influence the generated operations.
//
// The counters of a Stats value are reset by Differ.Reset.
// Each comparison must use its own Stats value; concurrent
// comparisons never share counters, which keeps the values
// collected by concurrent calls isolated from each other.
type Stats struct {
	// Digests counts the hash digests computed for the
	// values traversed during array matching and move/copy
	// candidate discovery. The digest of an array element
	// is computed at most once per comparison.
	Digests uint64
	// Comparisons counts the deep-equality checks performed
	// during array matching and move/copy candidate discovery.
	// Digest matches are always confirmed by such a check,
	// so that hash collisions cannot mispair elements.
	Comparisons uint64
}

// digestFunc computes the digest of a JSON value.
type digestFunc func(v interface{}) uint64

// arrayMatcher unifies element equality, hashing and longest
// common subsequence computation for a single array comparison.
// The digest of each element is computed at most once, when the
// matcher is created, and is shared by every subsequent step.
// A digest match is always confirmed by a deep-equality check,
// so that hash collisions can never mispair elements.
type arrayMatcher struct {
	src, tgt  []interface{}
	srcDigest []uint64
	tgtDigest []uint64
	stats     *Stats
}

// newArrayMatcher returns a matcher for the src and tgt slices.
// The digests of the elements of both slices are computed once,
// up front, using the given digest function.
func newArrayMatcher(src, tgt []interface{}, digestOf digestFunc, stats *Stats) *arrayMatcher {
	m := &arrayMatcher{
		src:       src,
		tgt:       tgt,
		srcDigest: make([]uint64, len(src)),
		tgtDigest: make([]uint64, len(tgt)),
		stats:     stats,
	}
	for i, v := range src {
		m.srcDigest[i] = digestOf(v)
	}
	for i, v := range tgt {
		m.tgtDigest[i] = digestOf(v)
	}
	return m
}

// equal reports whether src[i] and tgt[j] are deeply equal.
// The precomputed digests act as a fast negative filter; a
// digest match is always confirmed by a deep-equality check.
func (m *arrayMatcher) equal(i, j int) bool {
	if m.srcDigest[i] != m.tgtDigest[j] {
		return false
	}
	if m.stats != nil {
		m.stats.Comparisons++
	}
	return equalValues(m.src[i], m.tgt[j])
}

// equalValues reports whether src and tgt are deeply equal.
// Unlike deepEqual, values that are not valid JSON types do
// not cause a panic; they are compared with reflect.DeepEqual
// instead, so that a matcher can be exercised directly with
// arbitrary Go values. The public comparison entry points keep
// their regular error model: invalid JSON types are rejected
// before any array matching takes place.
func equalValues(src, tgt interface{}) bool {
	if jsonTypeSwitch(src) == jsonInvalid || jsonTypeSwitch(tgt) == jsonInvalid {
		return reflect.DeepEqual(src, tgt)
	}
	return deepEqual(src, tgt)
}

// lcsPairs computes the longest common subsequence of the
// matched slices and returns the index pairs of the LCS, that
// is, the indices into the source and target slices where the
// LCS items are located.
func (m *arrayMatcher) lcsPairs() [][2]int {
	src, tgt := m.src, m.tgt
	t := make([][]int, len(src)+1)

	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if m.equal(i-1, j-1) {
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
		case m.equal(i-1, j-1):
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

// unorderedMatch reports whether src and tgt hold the same
// elements, regardless of their order. The elements of the
// target slice are bucketed by digest, and each candidate
// pairing is confirmed by a deep-equality check, so that hash
// collisions cannot mispair elements and duplicate elements
// are matched one-to-one.
func unorderedMatch(src, tgt []interface{}, digestOf digestFunc, stats *Stats) bool {
	if len(src) != len(tgt) {
		return false
	}
	buckets := make(map[uint64][]int, len(tgt))
	for i, v := range tgt {
		k := digestOf(v)
		buckets[k] = append(buckets[k], i)
	}
	matched := make([]bool, len(tgt))
	for _, v := range src {
		k := digestOf(v)
		found := false
		for _, j := range buckets[k] {
			if matched[j] {
				continue
			}
			if stats != nil {
				stats.Comparisons++
			}
			if equalValues(v, tgt[j]) {
				matched[j] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
