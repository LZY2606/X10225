package jsondiff

import (
	"math/rand"
	"testing"
)

// constantDigest simulates a degenerate hasher for which every
// value collides. It is used to prove that the array matcher
// never pairs elements on digest equality alone.
func constantDigest(interface{}) uint64 { return 0 }

// realDigest returns the digest function backed by a fresh
// hasher, as used by the production comparison paths.
func realDigest() digestFunc {
	h := hasher{}
	return func(v interface{}) uint64 {
		return h.digest(v, false)
	}
}

// legacyLCS is a copy of the pre-refactor LCS algorithm, which
// performed a deep-equality check for every cell of the dynamic
// programming table. It is kept here as the reference baseline
// for correctness and comparison-count thresholds.
func legacyLCS(src, tgt []interface{}) (pairs [][2]int, comparisons uint64) {
	equal := func(a, b interface{}) bool {
		comparisons++
		return deepEqual(a, b)
	}
	t := make([][]int, len(src)+1)
	for i := 0; i <= len(src); i++ {
		t[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t); i++ {
		for j := 1; j < len(t[i]); j++ {
			if equal(src[i-1], tgt[j-1]) {
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
		case equal(src[i-1], tgt[j-1]):
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
	return s, comparisons
}

func checkLCSPairs(t *testing.T, src, tgt []interface{}, pairs [][2]int, wantLen int) {
	t.Helper()
	if len(pairs) != wantLen {
		t.Fatalf("LCS length mismatch: got %d pairs, want %d\npairs: %v", len(pairs), wantLen, pairs)
	}
	prevA, prevB := -1, -1
	for k, p := range pairs {
		a, b := p[0], p[1]
		if a <= prevA || b <= prevB {
			t.Fatalf("pair %d (%d,%d) breaks the increasing order of the LCS indices\npairs: %v", k, a, b, pairs)
		}
		if !deepEqual(src[a], tgt[b]) {
			t.Fatalf("pair %d (%d,%d) mismatches: src[%d]=%v != tgt[%d]=%v", k, a, b, a, src[a], b, tgt[b])
		}
		prevA, prevB = a, b
	}
}

func Test_arrayMatcher_equal_collisionSafe(t *testing.T) {
	src := []interface{}{"a", "a", "b"}
	tgt := []interface{}{"a", "b", "a"}

	var stats Stats
	m := newArrayMatcher(src, tgt, constantDigest, &stats)

	// Every digest collides, so equality must be decided
	// exclusively by the deep-equality confirmation.
	want := [][3]bool{
		{true, false, true},
		{true, false, true},
		{false, true, false},
	}
	for i := range src {
		for j := range tgt {
			if got := m.equal(i, j); got != want[i][j] {
				t.Errorf("equal(%d, %d) = %t, want %t (src[%d]=%v, tgt[%d]=%v)",
					i, j, got, want[i][j], i, src[i], j, tgt[j])
			}
		}
	}
	// All digests collide, so every call must have been
	// confirmed by a deep-equality check.
	if want := uint64(len(src) * len(tgt)); stats.Comparisons != want {
		t.Errorf("comparisons = %d, want %d: every colliding digest must be confirmed", stats.Comparisons, want)
	}
	// The injected digest function bypasses the differ,
	// so no digest is accounted to the stats.
	if stats.Digests != 0 {
		t.Errorf("digests = %d, want 0 with an injected digest function", stats.Digests)
	}
}

func Test_arrayMatcher_lcsPairs_collisionSafe(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src, tgt []interface{}
	}{
		{
			name: "duplicate values",
			src:  []interface{}{"a", "a", "b", "a"},
			tgt:  []interface{}{"a", "b", "a", "a"},
		},
		{
			name: "all identical",
			src:  []interface{}{"x", "x", "x"},
			tgt:  []interface{}{"x", "x"},
		},
		{
			name: "deep objects",
			src: []interface{}{
				map[string]interface{}{"k": map[string]interface{}{"v": []interface{}{1.0, 2.0}}},
				map[string]interface{}{"k": map[string]interface{}{"v": []interface{}{3.0}}},
			},
			tgt: []interface{}{
				map[string]interface{}{"k": map[string]interface{}{"v": []interface{}{3.0}}},
				map[string]interface{}{"k": map[string]interface{}{"v": []interface{}{1.0, 2.0}}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantPairs, _ := legacyLCS(tc.src, tc.tgt)
			m := newArrayMatcher(tc.src, tc.tgt, constantDigest, nil)
			pairs := m.lcsPairs()
			checkLCSPairs(t, tc.src, tc.tgt, pairs, len(wantPairs))
		})
	}
}

func Test_unorderedMatch_duplicates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		src, tgt []interface{}
		equal    bool
	}{
		{
			name: "same multiset, different order",
			src:  []interface{}{"a", "a", "b"},
			tgt:  []interface{}{"b", "a", "a"},
			equal: true,
		},
		{
			// Regression guard: a digest set without
			// per-element pairing would accept this
			// mismatch, because both sides hash to the
			// same set of digests.
			name:  "duplicates mispaired by digest set",
			src:   []interface{}{"a", "a", "b"},
			tgt:   []interface{}{"a", "b", "b"},
			equal: false,
		},
		{
			name:  "distinct elements",
			src:   []interface{}{"a", "b", "c"},
			tgt:   []interface{}{"a", "b", "d"},
			equal: false,
		},
		{
			name: "deep objects with duplicates",
			src: []interface{}{
				map[string]interface{}{"k": "v"},
				map[string]interface{}{"k": "v"},
			},
			tgt: []interface{}{
				map[string]interface{}{"k": "v"},
				map[string]interface{}{"k": "w"},
			},
			equal: false,
		},
	} {
		for _, digestOf := range []struct {
			name string
			fn   digestFunc
		}{
			{"real digest", realDigest()},
			{"colliding digest", constantDigest},
		} {
			t.Run(tc.name+"/"+digestOf.name, func(t *testing.T) {
				if got := unorderedMatch(tc.src, tc.tgt, digestOf.fn, nil); got != tc.equal {
					t.Errorf("unorderedMatch(%v, %v) = %t, want %t", tc.src, tc.tgt, got, tc.equal)
				}
			})
		}
	}
}

func Test_lcsPairs_matchesLegacy_seeded(t *testing.T) {
	const seed = 20260924
	rng := rand.New(rand.NewSource(seed))

	for iter := 0; iter < 200; iter++ {
		// Small alphabet to force many duplicate values.
		alphabet := []interface{}{"a", "b", "c", 1.0, 2.0, true, nil}
		mk := func() []interface{} {
			n := rng.Intn(12)
			s := make([]interface{}, n)
			for i := range s {
				s[i] = alphabet[rng.Intn(len(alphabet))]
			}
			return s
		}
		src, tgt := mk(), mk()

		wantPairs, _ := legacyLCS(src, tgt)
		gotPairs := newArrayMatcher(src, tgt, realDigest(), nil).lcsPairs()

		if len(gotPairs) != len(wantPairs) {
			t.Fatalf("seed=%d iter=%d: LCS length mismatch: got %d, want %d\nsrc=%v\ntgt=%v",
				seed, iter, len(gotPairs), len(wantPairs), src, tgt)
		}
		for k := range gotPairs {
			if gotPairs[k] != wantPairs[k] {
				t.Fatalf("seed=%d iter=%d: first diverging pair at %d: got %v, want %v\ngot:  %v\nwant: %v\nsrc=%v\ntgt=%v",
					seed, iter, k, gotPairs[k], wantPairs[k], gotPairs, wantPairs, src, tgt)
			}
		}
	}
}

func Test_arrayMatcher_comparisonBudget(t *testing.T) {
	const seed = 20260924
	rng := rand.New(rand.NewSource(seed))

	// Two arrays of n mostly-distinct elements with a small
	// number of shared values, so that the LCS is short and
	// the legacy algorithm performs O(n^2) deep-equality
	// checks while the matcher only confirms digest hits.
	const n = 64
	src := make([]interface{}, n)
	tgt := make([]interface{}, n)
	for i := 0; i < n; i++ {
		src[i] = rng.Intn(10000)
		tgt[i] = rng.Intn(10000)
	}
	// Plant a handful of shared elements.
	for _, i := range []int{3, 17, 29, 48} {
		tgt[i] = src[i]
	}
	// JSON-valid values.
	for i := range src {
		src[i] = float64(src[i].(int))
		tgt[i] = float64(tgt[i].(int))
	}

	var stats Stats
	h := hasher{}
	// Mirror the digest accounting of the production path
	// (see Differ.digestFunc).
	digestOf := func(v interface{}) uint64 {
		stats.Digests++
		return h.digest(v, false)
	}
	m := newArrayMatcher(src, tgt, digestOf, &stats)
	pairs := m.lcsPairs()

	_, legacyComparisons := legacyLCS(src, tgt)

	// Each element digest must be computed exactly once.
	if want := uint64(2 * n); stats.Digests != want {
		t.Errorf("digests = %d, want %d: element digests must be computed once", stats.Digests, want)
	}
	// The legacy baseline pays one deep-equality check per
	// cell of the DP table, plus the backtracking.
	if legacyComparisons < uint64(n*n) {
		t.Errorf("legacy comparisons = %d, want at least %d", legacyComparisons, n*n)
	}
	// The matcher only confirms digest hits; with mostly
	// distinct values this stays linear in the input size.
	if budget := uint64(4 * n); stats.Comparisons > budget {
		t.Errorf("comparisons = %d exceeds the linear budget %d (legacy: %d)",
			stats.Comparisons, budget, legacyComparisons)
	}
	if stats.Comparisons*8 > legacyComparisons {
		t.Errorf("comparisons = %d, want at most 1/8 of the legacy baseline %d",
			stats.Comparisons, legacyComparisons)
	}
	t.Logf("seed=%d lcs=%d comparisons: new=%d legacy=%d digests=%d",
		seed, len(pairs), stats.Comparisons, legacyComparisons, stats.Digests)
}
