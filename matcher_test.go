package jsondiff

import (
	"math/rand"
	"reflect"
	"testing"
)

// countingLCS is a test-local reference implementation of
// the LCS algorithm as it was previously implemented: each
// pair of elements is deep-compared while filling the
// dynamic programming table, and again while backtracking.
// It returns the LCS index pairs and the number of deep
// comparisons performed, and serves as the baseline for the
// comparison-count thresholds of the unified pairMatcher.
func countingLCS(src, tgt []interface{}) (pairs [][2]int, comparisons int) {
	equal := func(a, b interface{}) bool {
		comparisons++
		return deepEqual(a, b)
	}
	t_ := make([][]int, len(src)+1)
	for i := 0; i <= len(src); i++ {
		t_[i] = make([]int, len(tgt)+1)
	}
	for i := 1; i < len(t_); i++ {
		for j := 1; j < len(t_[i]); j++ {
			if equal(src[i-1], tgt[j-1]) {
				t_[i][j] = t_[i-1][j-1] + 1
			} else {
				t_[i][j] = max(t_[i-1][j], t_[i][j-1])
			}
		}
	}
	i, j := len(src), len(tgt)
	s := make([][2]int, 0, t_[i][j])
	for i > 0 && j > 0 {
		switch {
		case equal(src[i-1], tgt[j-1]):
			s = append(s, [2]int{i - 1, j - 1})
			i--
			j--
		case t_[i-1][j] > t_[i][j-1]:
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

// collidingDigest is a digest function that maps every
// value to the same hash, simulating systematic hash
// collisions. It flows through the exact same production
// matching path as real digests via the Differ.digestFn
// test hook.
func collidingDigest(interface{}, bool) uint64 { return 0 }

func TestPairMatcher_lcsPairs_matchesReference(t *testing.T) {
	const seed = 20260919
	rng := rand.New(rand.NewSource(seed))
	alphabet := []interface{}{"a", "b", "c", 1.0, 2.0, true, nil}

	for iter := 0; iter < 200; iter++ {
		src := make([]interface{}, rng.Intn(10))
		for i := range src {
			src[i] = alphabet[rng.Intn(len(alphabet))]
		}
		tgt := make([]interface{}, rng.Intn(10))
		for i := range tgt {
			tgt[i] = alphabet[rng.Intn(len(alphabet))]
		}
		want, _ := countingLCS(src, tgt)
		got := newPairMatcher(&Differ{}, src, tgt).lcsPairs()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d iter %d: lcsPairs() = %v, want %v\nsrc: %v\ntgt: %v",
				seed, iter, got, want, src, tgt)
		}
	}
}

func TestPairMatcher_evaluatesEachPairOnce(t *testing.T) {
	// With a systematically colliding hash, every pair is a
	// deep-comparison candidate; the matcher must evaluate
	// each of them exactly once, while the reference
	// implementation compares again during backtracking.
	src := []interface{}{"a", "b", "c", "a", "b", "c"}
	tgt := []interface{}{"c", "a", "b", "c", "a"}

	var stats Stats
	d := &Differ{digestFn: collidingDigest}
	d.stats = &stats
	pairs := newPairMatcher(d, src, tgt).lcsPairs()

	wantPairs, refComparisons := countingLCS(src, tgt)
	if !reflect.DeepEqual(pairs, wantPairs) {
		t.Fatalf("lcsPairs() = %v, want %v", pairs, wantPairs)
	}
	n, m := len(src), len(tgt)
	if stats.DeepEqualComparisons != n*m {
		t.Errorf("deep comparisons: got %d, want exactly %d (one per candidate pair)",
			stats.DeepEqualComparisons, n*m)
	}
	if stats.DeepEqualComparisons >= refComparisons {
		t.Errorf("deep comparisons: got %d, want less than reference implementation (%d)",
			stats.DeepEqualComparisons, refComparisons)
	}
	if stats.HashDigests != n+m {
		t.Errorf("hash digests: got %d, want %d (one per element)",
			stats.HashDigests, n+m)
	}
}

func TestPairMatcher_hashCollisionNeverMispairs(t *testing.T) {
	// All values collide on their hash; equality must be
	// decided by deep comparison alone, so that distinct
	// values are never paired together.
	src := []interface{}{1.0, 2.0, 2.0, 3.0}
	tgt := []interface{}{2.0, 2.0, 1.0, 4.0}

	d := &Differ{digestFn: collidingDigest}
	got := newPairMatcher(d, src, tgt).lcsPairs()
	want, _ := countingLCS(src, tgt)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lcsPairs() = %v, want %v", got, want)
	}
	for _, p := range got {
		if !deepEqual(src[p[0]], tgt[p[1]]) {
			t.Errorf("pair %v mispairs unequal values %v and %v", p, src[p[0]], tgt[p[1]])
		}
	}
}

func TestPairMatcher_duplicateValues(t *testing.T) {
	// Duplicate values must be paired monotonically: the
	// index pairs of the LCS are strictly increasing on both
	// sides, so that equal values are never crossed over.
	src := []interface{}{"x", "x", "x"}
	tgt := []interface{}{"x", "x"}

	got := newPairMatcher(&Differ{}, src, tgt).lcsPairs()
	want, _ := countingLCS(src, tgt)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lcsPairs() = %v, want %v", got, want)
	}
	for i := 1; i < len(got); i++ {
		if got[i][0] <= got[i-1][0] || got[i][1] <= got[i-1][1] {
			t.Errorf("pairs %v are not strictly increasing", got)
			break
		}
	}
}
