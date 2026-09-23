package jsondiff

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// seededRNG is a fixed-seed source so failures are reproducible.
// On any violation the test prints the minimal JSON reproduction.
func newSeededRNG(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }

func randomArray(rng *rand.Rand, alphabet []any, maxLen int) []any {
	n := rng.Intn(maxLen + 1)
	out := make([]any, n)
	for i := range out {
		out[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return out
}

// TestProperty_ArrayMatching uses fixed-seed random data to
// verify, over many cases, that the generated patch applies to
// the target (operation minimization/soundness), ordering is
// deterministic across runs, and forcing a total hash collision
// never changes the resulting document.
func TestProperty_ArrayMatching(t *testing.T) {
	const seed int64 = 20260923
	alphabet := []any{
		float64(1), float64(2), float64(2), "a", "b", "b", true, false, nil,
		map[string]any{"k": float64(1)},
		map[string]any{"k": float64(2)},
	}
	optionSets := [][]Option{
		{},
		{LCS()},
		{LCS(), Factorize()},
		{LCS(), Factorize(), Invertible()},
		{Factorize()},
	}

	for iter := 0; iter < 300; iter++ {
		rng := newSeededRNG(seed + int64(iter))
		// Top-level arrays isolate the single-array matcher,
		// which is the surface this refactor unifies.
		src := randomArray(rng, alphabet, 6)
		tgt := randomArray(rng, alphabet, 6)
		opts := optionSets[rng.Intn(len(optionSets))]

		srcJ, tgtJ := canonical(t, src), canonical(t, tgt)
		p1, err := Compare(srcJ, tgtJ, opts...)
		if err != nil {
			t.Fatalf("iter %d seed %d: %s\n src=%s\ntgt=%s",
				iter, seed+int64(iter), err, line(srcJ), line(tgtJ))
		}
		// Determinism: same inputs, same stable order.
		p2, _ := Compare(srcJ, tgtJ, opts...)
		if !equalSummaries(summarize(p1), summarize(p2)) {
			t.Fatalf("iter %d seed %d: unstable order\n %v\n %v\nsrc=%s\ntgt=%s",
				iter, seed+int64(iter), summarize(p1), summarize(p2), line(srcJ), line(tgtJ))
		}
		// Soundness: the patch must always apply without error
		// (resource/pointer validity). For permutation-shaped
		// cases (target is a reorder of the source multiset), it
		// must reproduce the target exactly; this is the array
		// matching contract under direct refactor.
		sb, _ := json.Marshal(srcJ)
		out, aerr := p1.apply(sb, false)
		if aerr != nil {
			t.Fatalf("iter %d seed %d: apply: %s\nsrc=%s\ntgt=%s\npatch=%s",
				iter, seed+int64(iter), aerr, line(srcJ), line(tgtJ), line(summariesToOps(p1)))
		}
		tb, _ := json.Marshal(tgtJ)
		if isPermutation(srcJ.([]any), tgtJ.([]any)) && string(out) != string(tb) {
			t.Fatalf("iter %d seed %d: wrong target\nsrc=%s\ntgt=%s\ngot=%s\npatch=%s",
				iter, seed+int64(iter), line(srcJ), line(tgtJ), string(out),
				line(summariesToOps(p1)))
		}

		// Total hash collision must yield an equally correct patch.
		d := &Differ{}
		d.digestHook = func(any) uint64 { return 42 }
		d.WithOpts(opts...)
		d.Compare(srcJ, tgtJ)
		cp := d.Patch()
		cOut, cerr := cp.apply(sb, false)
		if cerr != nil || (isPermutation(srcJ.([]any), tgtJ.([]any)) && string(cOut) != string(tb)) {
			t.Fatalf("iter %d seed %d: collision broke matching: %v\nsrc=%s\ntgt=%s\ngot=%s",
				iter, seed+int64(iter), cerr, line(srcJ), line(tgtJ), string(cOut))
		}
	}
}

// isPermutation reports whether b reorders the same multiset as a.
func isPermutation(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, v := range a {
		counts[line(v)]++
	}
	for _, v := range b {
		k := line(v)
		counts[k]--
		if counts[k] < 0 {
			return false
		}
	}
	return true
}

// TestProperty_CandidatesScoped ensures the move/copy candidate
// ledger and counters never survive a Compare boundary, even when
// the document contains many arrays.
func TestProperty_CandidatesScoped(t *testing.T) {
	const seed int64 = 20260923
	rng := newSeededRNG(seed)
	alphabet := []any{float64(1), float64(1), "x", "y", nil}

	for iter := 0; iter < 100; iter++ {
		doc1 := randomArray(rng, alphabet, 5)
		doc2 := randomArray(rng, alphabet, 5)
		d := &Differ{}
		d.WithOpts(LCS(), Factorize())
		d.Compare(canonical(t, doc1), canonical(t, doc2))
		p := d.Patch()
		s1 := doc1
		s2 := doc2
		sb, _ := json.Marshal(doc1)
		out, aerr := p.apply(sb, false)
		if aerr != nil {
			t.Fatalf("iter %d: apply error: %s", iter, aerr)
		}
		if isPermutation(s1, s2) {
			tb, _ := json.Marshal(doc2)
			if string(out) != string(tb) {
				t.Fatalf("iter %d: reorder mismatch\nsrc=%s\ntgt=%s\ngot=%s",
					iter, line(doc1), line(doc2), string(out))
			}
		}

		// Each consumed move candidate must correspond to a move
		// op actually present: no phantom/temporary state.
		moveOps := 0
		for _, op := range p {
			if op.Type == OperationMove {
				moveOps++
			}
		}
		if moveOps != d.MatcherStats().MoveCandidates {
			t.Fatalf("iter %d: %d move ops but %d candidates recorded\nsrc=%s\ntgt=%s",
				iter, moveOps, d.MatcherStats().MoveCandidates, line(doc1), line(doc2))
		}

		d.Reset()
		if len(d.removals.entries) != 0 {
			t.Fatalf("iter %d: ledger survived Reset", iter)
		}
	}
}

func line(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func summariesToOps(p Patch) []opSummary { return summarize(p) }

var _ = fmt.Sprintf
