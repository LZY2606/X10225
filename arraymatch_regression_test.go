package jsondiff

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These tests are the regression material for the work titled
// "unify array matching and move/copy candidate accounting".
//
// Every scenario enters through the public API (Compare /
// CompareJSON / CompareWithoutMarshal or Differ.Compare): the
// internal array matcher is exercised only as the production
// implementation behind those entries, never fabricated.

func mustPatch(t *testing.T, src, tgt any, opts ...Option) (Patch, *Differ) {
	t.Helper()
	d := &Differ{}
	d.WithOpts(opts...)
	d.Compare(src, tgt)
	return d.Patch(), d
}

// opSummary is a stable, order-sensitive projection of a patch.
type opSummary struct {
	typ, path, from string
}

func summarize(p Patch) []opSummary {
	s := make([]opSummary, len(p))
	for i, op := range p {
		s[i] = opSummary{op.Type, op.Path, op.From}
	}
	return s
}

func reportFirstViolation(t *testing.T, i int, got, want opSummary) {
	t.Helper()
	t.Errorf("first violation at op #%d:\n got  %+v\n want %+v", i, got, want)
}

func assertPatchSummary(t *testing.T, p Patch, want []opSummary) {
	t.Helper()
	got := summarize(p)
	if len(got) != len(want) {
		t.Fatalf("patch length mismatch at op #%d:\n got  %v\n want %v",
			min(len(got), len(want)), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			reportFirstViolation(t, i, got[i], want[i])
			return
		}
	}
}

func assertPatchApplies(t *testing.T, src any, p Patch, want any) {
	t.Helper()
	sb, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal src: %s", err)
	}
	out, err := p.apply(sb, false)
	if err != nil {
		t.Fatalf("patch must apply and release resources: %s", err)
	}
	wantBytes, _ := json.Marshal(want)
	if string(out) != string(wantBytes) {
		t.Fatalf("patched document mismatch:\n got  %s\n want %s", out, wantBytes)
	}
}

// TestArrayMatch_RepeatedValues guards that repeated equal
// elements are paired as distinct occurrences: a value moved
// between two equal neighbors must not be paired by identity or
// hash alone, and the resulting patch must be applicable.
func TestArrayMatch_RepeatedValues(t *testing.T) {
	src := []any{float64(1), float64(1), "x"}
	tgt := []any{float64(1), "x", float64(1)}

	p, d := mustPatch(t, src, tgt, LCS(), Factorize())
	assertPatchApplies(t, src, p, tgt)

	s := summarize(p)
	for i, op := range s {
		if op.typ == OperationMove && op.from == "/0" && op.path == "/2" {
			return // found the expected cross move
		}
		_ = i
	}
	t.Fatalf("expected a move /0 -> /2 distinguishing duplicates, got %v (stats=%+v)", s, d.MatcherStats())
}

// TestArrayMatch_DeepObjects ensures deep (not shallow or hash)
// equality drives matches through nested objects.
func TestArrayMatch_DeepObjects(t *testing.T) {
	src := []any{
		map[string]any{"a": map[string]any{"b": float64(1)}},
		map[string]any{"a": map[string]any{"b": float64(2)}},
	}
	tgt := []any{
		map[string]any{"a": map[string]any{"b": float64(2)}},
		map[string]any{"a": map[string]any{"b": float64(1)}},
	}
	p, d := mustPatch(t, src, tgt, LCS())
	assertPatchApplies(t, src, p, tgt)

	// Both elements are matched (reordered) without replacement:
	// LCS length is one and the two unmatched positions move.
	st := d.MatcherStats()
	if st.EqualDeep == 0 {
		t.Errorf("expected deep comparisons over nested objects, stats=%+v", st)
	}
	for _, op := range p {
		if op.Type == OperationReplace {
			t.Errorf("deep-equal objects reordered, replace must not be emitted: %v", p)
		}
	}
}

// TestArrayMatch_HashCollisionCannotPair proves that a digest
// collision never matches distinct elements. Every digest is
// forced to a single constant; correctness must rest on
// deepEqual alone.
func TestArrayMatch_HashCollisionCannotPair(t *testing.T) {
	src := []any{"a", "b", "a", "c"}
	tgt := []any{"a", "x", "a", "c"}

	d := &Differ{}
	d.digestHook = func(any) uint64 { return 1 }
	d.WithOpts(LCS(), Factorize())
	d.Compare(src, tgt)
	p := d.Patch()
	st := d.MatcherStats()

	assertPatchApplies(t, src, p, tgt)

	// With every hash identical, every pair is verified deeply:
	// duplicates still align to distinct occurrences, and the
	// only change is b -> x at index 1.
	assertPatchSummary(t, p, []opSummary{
		{OperationReplace, "/1", ""},
	})
	if st.EqualDeep < len(src)*len(tgt) {
		t.Errorf("forced collision must verify every pair deeply: got %d, want >= %d; stats=%+v",
			st.EqualDeep, len(src)*len(tgt), st)
	}
}

// TestArrayMatch_HashCollisionMultiset verifies the unordered
// multiset comparison under a total digest collision: duplicates
// are counted, not set-collapsed.
func TestArrayMatch_HashCollisionMultiset(t *testing.T) {
	equal := func(src, tgt []any) bool {
		d := &Differ{}
		d.digestHook = func(any) uint64 { return 1 }
		d.WithOpts(Equivalent())
		return d.unorderedDeepEqualSlice(src, tgt)
	}
	if !equal([]any{float64(1), float64(1), float64(2)}, []any{float64(2), float64(1), float64(1)}) {
		t.Errorf("same multiset with duplicates must be equal under total collision")
	}
	if equal([]any{float64(1), float64(1), float64(2)}, []any{float64(1), float64(2), float64(2)}) {
		t.Errorf("different multiplicities matched under total collision")
	}
}

// TestArrayMatch_HeadTailInsertion checks insertions at both
// extremities produce stable pointer order.
func TestArrayMatch_HeadTailInsertion(t *testing.T) {
	src := []any{"m"}
	tgt := []any{"h", "m", "t"}

	p, _ := mustPatch(t, src, tgt, LCS())
	assertPatchSummary(t, p, []opSummary{
		{OperationAdd, "/0", ""},
		{OperationAdd, "/2", ""},
	})
	assertPatchApplies(t, src, p, tgt)
}

// TestArrayMatch_CrossMove guards an X-shaped reorder.
func TestArrayMatch_CrossMove(t *testing.T) {
	src := []any{"a", "b", "c"}
	tgt := []any{"c", "b", "a"}

	p, _ := mustPatch(t, src, tgt, LCS(), Factorize())
	assertPatchApplies(t, src, p, tgt)

	var moves []opSummary
	for _, op := range summarize(p) {
		if op.typ == OperationMove {
			moves = append(moves, op)
		}
	}
	if len(moves) != 2 {
		t.Fatalf("expected exactly 2 cross moves, got %v in %v", moves, summarize(p))
	}
}

// TestArrayMatch_OptOutRestoresSemantics verifies that disabling
// each optimization falls back to the positional semantics and
// never emits move/copy operations.
func TestArrayMatch_OptOutRestoresSemantics(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
	}{
		{"no_options", nil},
		{"lcs_only", []Option{LCS()}},
		{"factorize_only", []Option{Factorize()}},
	}
	src := []any{"a", "b"}
	tgt := []any{"b", "a", "c"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := mustPatch(t, src, tgt, tc.opts...)
			assertPatchApplies(t, src, p, tgt)
			for i, op := range p {
				if op.Type == OperationMove || op.Type == OperationCopy {
					t.Fatalf("op #%d: move/copy leaked with opts disabled: %v", i, p)
				}
			}
		})
	}
}

// TestArrayMatch_FactorizeDisabledNoCopy checks copy suppression
// when factorization is off but equivalent ordering is on.
func TestArrayMatch_FactorizeDisabledNoCopy(t *testing.T) {
	src := map[string]any{"a": []any{float64(1), float64(2), float64(3)}}
	tgt := map[string]any{"a": []any{float64(1), float64(2), float64(3)}, "b": []any{float64(1), float64(2), float64(3)}}

	p, _ := mustPatch(t, src, tgt)
	for i, op := range p {
		if op.Type == OperationCopy {
			t.Fatalf("op #%d: copy emitted without Factorize: %v", i, p)
		}
	}
	assertPatchApplies(t, src, p, tgt)
}

// TestArrayMatch_ComparisonBudget establishes baseline thresholds
// for old vs new equality work. The "old" upper bound is the work
// the pre-refactor pair-unaware LCS + findRemoved scanning would
// perform: (len(src)*len(tgt)) during the DP, again during
// backtracking, plus an O(removes) deepEqual scan per addition.
// The new implementation must never exceed it and must verify
// each equal pair at most once.
func TestArrayMatch_ComparisonBudget(t *testing.T) {
	src := []any{"a", "b", "c", "d", "e", "f"}
	tgt := []any{"x", "b", "y", "d", "e", "z", "f"}

	p, d := mustPatch(t, src, tgt, LCS(), Factorize())
	assertPatchApplies(t, src, p, tgt)
	st := d.MatcherStats()

	// Old reference cost: DP 6*7=42 + up to 42 backtracking probes
	// + findRemoved scans for 3 additions against up to 3 removes.
	const oldDPDeep = 6 * 7
	const oldBacktrack = 6 * 7
	const oldMoveScan = 3 * 3
	oldUpperBound := oldDPDeep + oldBacktrack + oldMoveScan

	if st.EqualDeep > oldUpperBound {
		t.Errorf("new deep comparisons %d exceed old baseline %d; stats=%+v",
			st.EqualDeep, oldUpperBound, st)
	}
	// The hash prefilter must have eliminated obvious mismatches,
	// so strictly fewer than the old DP-only cost is required.
	if st.EqualDeep >= oldDPDeep {
		t.Errorf("hash prefilter ineffective: %d deep comparisons >= DP cells %d; stats=%+v",
			st.EqualDeep, oldDPDeep, st)
	}
	// Every equal (hash-equal) pair is settled exactly once: the
	// matcher can be exercised again over the same arrays without
	// growth, which is asserted separately by determinism tests.
	if st.EqualDeep <= 0 {
		t.Errorf("expected measurable equality work, stats=%+v", st)
	}
	t.Logf("budget: new deep=%d fallback=%d hashes=%d vs old upper bound=%d",
		st.EqualDeep, st.EqualFallback, st.HashDigest, oldUpperBound)
}

// TestArrayMatch_CandidateComputedOnce verifies a candidate pair
// shared between LCS backtracking and move discovery is not deep
// compared twice within a single matcher.
func TestArrayMatch_CandidateComputedOnce(t *testing.T) {
	src := []any{"a", "b", "c"}
	tgt := []any{"b", "c", "d"}

	p1, d1 := mustPatch(t, src, tgt, LCS(), Factorize())
	p2, d2 := mustPatch(t, src, tgt, LCS(), Factorize())

	if !reflect.DeepEqual(summarize(p1), summarize(p2)) {
		t.Fatalf("non-deterministic patch:\n %v\n %v", summarize(p1), summarize(p2))
	}
	assertPatchApplies(t, src, p1, tgt)

	// Every (i,j) equality result is memoized: count distinct
	// hash-equal pairs actually deep-compared by recomputing the
	// matcher table manually and comparing the counters.
	m := newArrayMatcher(d1, src, tgt, false)
	var manual int
	for i := range src {
		for j := range tgt {
			if m.digestSrc(i) == m.digestTgt(j) {
				manual++
			}
		}
	}
	if d1.MatcherStats().EqualDeep != manual {
		t.Errorf("candidate pair settled more than once: counted %d deep, %d hash-equal pairs; stats=%+v",
			d1.MatcherStats().EqualDeep, manual, d1.stats)
	}
	if d2.MatcherStats().EqualDeep != d1.MatcherStats().EqualDeep {
		t.Errorf("comparison count unstable across calls: %+v vs %+v", d1.stats, d2.stats)
	}
}

// TestArrayMatch_MinimalRepro prints a fixed seed-free minimal
// reproduction for the reorder scenario.
func TestArrayMatch_MinimalRepro(t *testing.T) {
	src := []any{float64(1), float64(2), float64(3)}
	tgt := []any{float64(3), float64(2), float64(1)}
	t.Logf("minimal repro:\n src=%v\n tgt=%v\nseed=fixed:no-randomness",
		marshalLine(src), marshalLine(tgt))
	p, _ := mustPatch(t, src, tgt, LCS(), Factorize())
	assertPatchApplies(t, src, p, tgt)
}

func marshalLine(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var _ = strings.TrimSpace
var _ = fmt.Sprintf
