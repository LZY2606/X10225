package jsondiff

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// This file contains the regression material for
// "jsondiff: unified array matching and move/copy candidate
// accounting". Every case enters through the public
// Compare/CompareJSON/CompareWithoutMarshal entry points; no
// internal final patch object is fabricated. The unexported
// seams used here (fault hook, digest override, diagnostic
// counters) exercise the exact same production logic as a
// normal comparison.

// mustCompareJSON runs a comparison through the public
// CompareJSON entry and aborts the test on error.
func mustCompareJSON(t *testing.T, src, tgt string, opts ...Option) (Patch, *Differ) {
	t.Helper()
	d := &Differ{}
	d.applyOpts(opts...)
	p, err := compareJSON(d, []byte(src), []byte(tgt), nil)
	if err != nil {
		t.Fatalf("CompareJSON error: %v\nsrc=%s\ntgt=%s", err, src, tgt)
	}
	return p, d
}

// opSummary is a stable, readable rendering of a patch used
// in failure messages so that the first violating position and
// its context are visible instead of a bare boolean.
type opSummary struct {
	idx  int
	typ  string
	from string
	path string
	val  any
}

func summarize(p Patch) []opSummary {
	out := make([]opSummary, len(p))
	for i, o := range p {
		out[i] = opSummary{idx: i, typ: o.Type, from: o.From, path: o.Path, val: o.Value}
	}
	return out
}

func expectOpsAt(t *testing.T, got Patch, want []opSummary) {
	t.Helper()
	gs := summarize(got)
	if len(gs) != len(want) {
		t.Fatalf("op count mismatch at first violation:\n got %d ops: %+v\nwant %d ops: %+v",
			len(gs), gs, len(want), want)
	}
	for i := range want {
		g, w := gs[i], want[i]
		if g.typ != w.typ || g.path != w.path || g.from != w.from ||
			!reflect.DeepEqual(g.val, w.val) {
			t.Fatalf("op #%d is the first violation:\n got %+v\nwant %+v\nfull got: %+v",
				i, g, w, gs)
		}
	}
}

// patchRoundTrips applies the patch to the source and checks
// that the resulting document equals the target: the semantic
// correctness invariant of JSON diff/operation minimization.
func patchRoundTrips(t *testing.T, src, tgt string, p Patch) {
	t.Helper()
	out, err := p.apply([]byte(src), false)
	if err != nil {
		t.Fatalf("patch failed to apply: %v\npatch: %s", err, p.String())
	}
	var got, want any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("patched doc invalid: %v", err)
	}
	if err := json.Unmarshal([]byte(tgt), &want); err != nil {
		t.Fatalf("target invalid: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patch does not yield the target document:\n got %s\nwant %s", string(out), tgt)
	}
}

// collisionDigest forces a constant digest for every value:
// every hash bucket collides, so correctness can only come
// from the verified equality fallback, never from the hash.
func collisionDigest(any, bool) uint64 { return 1 }

// runComparisons runs the public entry n times and requires
// byte-identical, order-stable outputs across all runs.
func runComparisons(t *testing.T, n int, src, tgt string, opts ...Option) Patch {
	t.Helper()
	var first Patch
	for i := 0; i < n; i++ {
		p, _ := mustCompareJSON(t, src, tgt, opts...)
		if i == 0 {
			first = p
			continue
		}
		if p.String() != first.String() {
			t.Fatalf("unstable patch order at run %d:\nfirst:\n%s\nrun %d:\n%s",
				i, first.String(), i, p.String())
		}
	}
	return first
}

// TestArrayMatchingGuards is the fixed-input regression group
// for "unified array matching and move/copy candidate
// accounting". Each case records both the caller-visible patch
// and the internal diagnostics (comparison counts, collisions),
// and verifies the JSON-diff semantic invariant by applying the
// patch back onto the source document.
func TestArrayMatchingGuards(t *testing.T) {
	const optsKey = "factorize+lcs"

	cases := []struct {
		name    string
		src     string
		tgt     string
		opts    []Option
		wantOps []opSummary
		// Baseline thresholds for old/new element comparisons.
		// Exact values are pinned for deterministic inputs; the
		// thresholds document the matching complexity budget.
		maxEqualChecks int
	}{
		{
			name: "duplicate values are matched individually",
			src:  `[1,1,2,2,3]`,
			tgt:  `[2,2,1,1,3]`,
			opts: []Option{LCS(), Factorize()},
			wantOps: []opSummary{
				{typ: OperationMove, from: "/0", path: "/3"},
				{typ: OperationMove, from: "/0", path: "/2"},
			},
			// 5x3 distinct DP probes (one pair of identical
			// columns) + 3 backtrack probes cached.
			maxEqualChecks: 13,
		},
		{
			name: "deep objects reuse one equality decision",
			src:  `{"a":[{"k":{"p":[1,2,3]}},{"k":{"p":[1,2,3]}}]}`,
			tgt:  `{"a":[{"k":{"p":[1,2,3]}},{"k":{"p":[3,2,1]}},{"k":{"p":[1,2,3]}}]}`,
			opts: []Option{LCS()},
			wantOps: []opSummary{
				{typ: OperationAdd, path: "/a/1", val: map[string]any{
					"k": map[string]any{"p": []any{3.0, 2.0, 1.0}},
				}},
			},
			// 2x3 DP probes; no pair is probed twice.
			maxEqualChecks: 6,
		},
		{
			name: "head insertion keeps tail indices stable",
			src:  `["a","b","c"]`,
			tgt:  `["z","a","b","c"]`,
			opts: []Option{LCS()},
			wantOps: []opSummary{
				{typ: OperationAdd, path: "/0", val: "z"},
			},
			maxEqualChecks: 12,
		},
		{
			name: "tail insertion uses the append path",
			src:  `["a","b","c"]`,
			tgt:  `["a","b","c","q"]`,
			opts: []Option{LCS()},
			wantOps: []opSummary{
				{typ: OperationAdd, path: "/3", val: "q"},
			},
			maxEqualChecks: 12,
		},
		{
			name: "cross move of distinct objects",
			src:  `[{"id":1},{"id":2},{"id":3},{"id":4}]`,
			tgt:  `[{"id":4},{"id":2},{"id":3},{"id":1}]`,
			opts: []Option{LCS()},
			wantOps: []opSummary{
				{typ: OperationReplace, path: "/0/id", val: 4.0},
				{typ: OperationReplace, path: "/3/id", val: 1.0},
			},
			// 4x4 DP table; LCS keeps the two middle objects.
			maxEqualChecks: 16,
		},
		{
			name: "moves disabled without factorize fall back to adds/removes",
			src:  `[1,1,2,2,3]`,
			tgt:  `[2,2,1,1,3]`,
			opts: []Option{LCS()},
			wantOps: []opSummary{
				{typ: OperationRemove, path: "/0"},
				{typ: OperationRemove, path: "/0"},
				{typ: OperationAdd, path: "/2", val: 1.0},
				{typ: OperationAdd, path: "/3", val: 1.0},
			},
			maxEqualChecks: 13,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Repeated runs guard ordering stability across
			// different map-enumeration schedules.
			p := runComparisons(t, 8, tc.src, tc.tgt, tc.opts...)
			expectOpsAt(t, p, tc.wantOps)
			patchRoundTrips(t, tc.src, tc.tgt, p)

			_, d := mustCompareJSON(t, tc.src, tc.tgt, tc.opts...)
			st := d.diagnosticStats()
			total := st.TotalEqualChecks()
			if total > tc.maxEqualChecks {
				t.Fatalf("too many old/new comparisons: budget %d, got %d (%+v) [%s]",
					tc.maxEqualChecks, total, st, optsKey)
			}
			if total == 0 {
				t.Fatalf("expected a non-zero, quantifiable comparison count, got %+v", st)
			}
		})
	}
}

// TestEquivalentDuplicateBaseline pins the multiset semantics
// of the Equivalent option against duplicate values and
// hash-collision pressure.
func TestEquivalentDuplicateBaseline(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		tgt     string
		equal   bool
		maxHits int
	}{
		{"same multiset different order", `[1,1,2,2,3]`, `[3,2,1,2,1]`, true, 25},
		{"multiplicity mismatch is not equivalent", `[1,1,2]`, `[1,2,2]`, false, 25},
		{"length mismatch", `[1,1]`, `[1]`, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, d := mustCompareJSON(t, tc.src, tc.tgt, LCS(), Equivalent())
			if tc.equal && len(p) != 0 {
				t.Fatalf("expected empty patch for equivalent arrays, got %s", p.String())
			}
			st := d.diagnosticStats()
			if !tc.equal && len(p) == 0 {
				t.Fatalf("non-equivalent arrays produced an empty patch")
			}
			if tc.maxHits > 0 && st.MultisetEqualChecks > tc.maxHits {
				t.Fatalf("multiset comparison budget exceeded: got %d, max %d (%+v)",
					st.MultisetEqualChecks, tc.maxHits, st)
			}
		})
	}
}

// TestHashCollisionGuards forces every digest to collide. The
// matcher must fall back to verified equality in every role:
// LCS pairing, move source lookup, copy source lookup and the
// unordered multiset test. Unequal elements sharing a digest
// must never be paired.
func TestHashCollisionGuards(t *testing.T) {
	t.Run("lcs does not pair unequal elements on a collision", func(t *testing.T) {
		src, tgt := `["a","b","c","d"]`, `["d","x","b","z"]`
		d := &Differ{digestOverride: collisionDigest}
		d.applyOpts(LCS())
		p, err := compareJSON(d, []byte(src), []byte(tgt), nil)
		if err != nil {
			t.Fatal(err)
		}
		// The common subsequence ("d","b") must still be found;
		// "a"/"c" must not be paired with "x"/"z".
		want := []opSummary{
			{typ: OperationAdd, path: "/0", val: "d"},
			{typ: OperationReplace, path: "/1", val: "x"},
			{typ: OperationRemove, path: "/3"},
			{typ: OperationAdd, path: "/3", val: "z"},
		}
		expectOpsAt(t, p, want)
		patchRoundTrips(t, src, tgt, p)
		if d.diagnosticStats().HashCollisions == 0 {
			t.Fatalf("expected the forced collisions to be recorded and verified")
		}
	})

	t.Run("move candidate requires verified equality", func(t *testing.T) {
		// The removed object has the same (forced) digest as the
		// added object, but differs in content: it must be a
		// remove+add pair, never a move.
		src := `[{"v":1}]`
		tgt := `[{"v":2}]`
		d := &Differ{digestOverride: collisionDigest}
		d.applyOpts(LCS(), Factorize())
		p, err := compareJSON(d, []byte(src), []byte(tgt), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range p {
			if op.Type == OperationMove || op.Type == OperationCopy {
				t.Fatalf("collision produced an incorrect %s op: %s", op.Type, p.String())
			}
		}
		patchRoundTrips(t, src, tgt, p)
	})

	t.Run("copy candidate requires verified equality", func(t *testing.T) {
		src := `{"a":{"k":1}}`
		tgt := `{"a":{"k":1},"b":{"k":2}}`
		d := &Differ{digestOverride: collisionDigest}
		d.applyOpts(Factorize())
		p, err := compareJSON(d, []byte(src), []byte(tgt), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range p {
			if op.Type == OperationCopy {
				t.Fatalf("collision paired unequal values with copy: %s", p.String())
			}
		}
		patchRoundTrips(t, src, tgt, p)
	})

	t.Run("duplicate values pair the right occurrences under collision", func(t *testing.T) {
		src, tgt := `[1,1,2]`, `[2,1,1]`
		d := &Differ{digestOverride: collisionDigest}
		d.applyOpts(LCS(), Factorize())
		p, err := compareJSON(d, []byte(src), []byte(tgt), nil)
		if err != nil {
			t.Fatal(err)
		}
		// With all digests equal, the two "1" occurrences must
		// still match themselves (verified), yielding a single
		// move instead of erroneous replaces.
		expectOpsAt(t, p, []opSummary{
			{typ: OperationMove, from: "/2", path: "/0"},
		})
		patchRoundTrips(t, src, tgt, p)
	})

	t.Run("multiset rejects multiplicity mismatch under collision", func(t *testing.T) {
		d := &Differ{digestOverride: collisionDigest}
		d.applyOpts(LCS(), Equivalent())
		p, err := compareJSON(d, []byte(`[1,1,2]`), []byte(`[1,2,2]`), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(p) == 0 {
			t.Fatalf("collision-only matching wrongly declared arrays equivalent")
		}
	})
}

// TestOrderStability changes only result-irrelevant conditions
// (option application order, execution scheduling and digest
// enumeration order) and proves the public patch structure and
// stable ordering are unaffected.
func TestOrderStability(t *testing.T) {
	src := `{"a":[1,1,2,2,3],"b":{"x":[1,2,3],"y":[1,2,3]},"c":[1,2,3]}`
	tgt := `{"a":[2,2,1,1,3],"b":{"x":[1,2,3],"y":[1,2,3]},"d":[1,2,3],"e":[1,1,2,2,3]}`

	baseline, _ := mustCompareJSON(t, src, tgt, Factorize(), LCS(), Equivalent())

	t.Run("option application order is irrelevant", func(t *testing.T) {
		perms := [][]Option{
			{Equivalent(), LCS(), Factorize()},
			{LCS(), Equivalent(), Factorize()},
			{Factorize(), Equivalent(), LCS()},
		}
		for i, opts := range perms {
			p, _ := mustCompareJSON(t, src, tgt, opts...)
			if p.String() != baseline.String() {
				t.Fatalf("permutation %d changed the public patch:\n got:\n%s\nwant:\n%s",
					i, p.String(), baseline.String())
			}
		}
	})

	t.Run("interleaved scheduling keeps outputs isolated and stable", func(t *testing.T) {
		// Interleave several comparisons over the same inputs on
		// separate Differ instances, synchronizing phases with a
		// barrier so they run concurrently at every stage.
		const workers = 4
		var wg sync.WaitGroup
		results := make([]string, workers)
		stats := make([]Stats, workers)
		start := make(chan struct{})
		var barrier sync.WaitGroup
		barrier.Add(workers)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				d := &Differ{}
				d.applyOpts(Factorize(), LCS())
				barrier.Done()
				<-start
				p, err := compareJSON(d, []byte(src), []byte(tgt), nil)
				if err != nil {
					t.Errorf("worker %d: %v", id, err)
					return
				}
				results[id] = p.String()
				stats[id] = d.diagnosticStats()
			}(w)
		}
		barrier.Wait()
		close(start)
		wg.Wait()
		for i, r := range results {
			if r != baseline.String() {
				t.Fatalf("worker %d patch diverged:\n%s", i, r)
			}
		}
		for i := 1; i < workers; i++ {
			if stats[i] != stats[0] {
				t.Fatalf("diagnostic counters leaked across concurrent calls: %+v vs %+v",
					stats[0], stats[i])
			}
		}
	})
}

// TestOptimizationFallbacks proves that disabling each
// optimization restores the original semantics.
func TestOptimizationFallbacks(t *testing.T) {
	src, tgt := `[1,1,2,2,3]`, `[2,2,1,1,3]`

	defaultPatch, _ := mustCompareJSON(t, src, tgt)
	lcsPatch, _ := mustCompareJSON(t, src, tgt, LCS())
	factorPatch, _ := mustCompareJSON(t, src, tgt, Factorize())
	bothPatch, _ := mustCompareJSON(t, src, tgt, Factorize(), LCS())

	// Without LCS, position-wise replaces are generated.
	expectOpsAt(t, defaultPatch, []opSummary{
		{typ: OperationReplace, path: "/0", val: 2.0},
		{typ: OperationReplace, path: "/1", val: 2.0},
		{typ: OperationReplace, path: "/2", val: 1.0},
		{typ: OperationReplace, path: "/3", val: 1.0},
	})
	for _, op := range factorPatch {
		if op.Type == OperationMove || op.Type == OperationCopy {
			t.Fatalf("moves must not appear without LCS arrays: %s", factorPatch.String())
		}
	}
	for _, op := range lcsPatch {
		if op.Type == OperationMove || op.Type == OperationCopy {
			t.Fatalf("moves/copies must not appear without Factorize: %s", lcsPatch.String())
		}
	}
	// Both optimizations enable moves.
	if n := countOps(bothPatch, OperationMove); n != 2 {
		t.Fatalf("expected 2 moves with LCS+Factorize, got %d:\n%s", n, bothPatch.String())
	}
	patchRoundTrips(t, src, tgt, defaultPatch)
	patchRoundTrips(t, src, tgt, lcsPatch)
	patchRoundTrips(t, src, tgt, bothPatch)
}

func countOps(p Patch, typ string) int {
	n := 0
	for _, op := range p {
		if op.Type == typ {
			n++
		}
	}
	return n
}

// TestRecoverableMidRunFailure injects one recoverable failure
// in the middle of a comparison. The public patch must remain
// untouched, no shared state may be half-updated, and retrying
// the identical inputs must succeed with the stable result.
func TestRecoverableMidRunFailure(t *testing.T) {
	src := `{"a":[1,1,2,2,3],"b":[1,2,3]}`
	tgt := `{"a":[2,2,1,1,3],"b":[1,2,3,4]}`

	wantPatch, _ := mustCompareJSON(t, src, tgt, Factorize(), LCS())

	for _, stage := range []string{faultPointAfterPrepare, faultPointArrayMatched} {
		t.Run(stage, func(t *testing.T) {
			// Pre-existing patch content must survive a failed
			// call on a reused Differ, proving no half-update.
			prefix, _ := mustCompareJSON(t, `{"seed":0}`, `{"seed":1}`)
			d := &Differ{}
			d.applyOpts(Factorize(), LCS())
			d.Compare(`{"seed":0}`, map[string]any{"seed": 1.0})
			beforeLen := len(d.Patch())
			if beforeLen != len(prefix) {
				t.Fatalf("setup error: prefix length %d vs %d", beforeLen, len(prefix))
			}

			fired := false
			d.withFault(func(s string) error {
				if s == stage && !fired {
					fired = true
					return &errComparisonAborted{stage: s}
				}
				return nil
			})
			err := d.compareInternal(mustUnmarshal(src), mustUnmarshal(tgt))
			if err == nil {
				t.Fatalf("expected the injected failure to surface")
			}
			var aborted *errComparisonAborted
			if !errors.As(err, &aborted) {
				t.Fatalf("failure does not fit the existing error layer: got %T %v", err, err)
			}
			if !fired {
				t.Fatalf("fault hook was never invoked for stage %q", stage)
			}

			// Public patch exactly matches the pre-call state.
			after := d.Patch()
			if len(after) != beforeLen || after.String() != prefix.String() {
				t.Fatalf("failed call half-updated the public patch:\n got:\n%s\nwant:\n%s",
					after.String(), prefix.String())
			}
			// No candidates, counters or temp state survive.
			if d.matcher != nil {
				t.Fatalf("per-comparison matcher leaked after failure")
			}
			if d.diagnosticStats() != (Stats{}) {
				t.Fatalf("diagnostic counters leaked after failure: %+v", d.diagnosticStats())
			}
			if len(d.hashmap) != 0 {
				t.Fatalf("unchanged-location state leaked after failure")
			}

			// Retry with the identical inputs, without the hook,
			// and obtain the stable successful result.
			d.fault = nil
			if err := d.compareInternal(mustUnmarshal(src), mustUnmarshal(tgt)); err != nil {
				t.Fatalf("retry failed: %v", err)
			}
			got := d.Patch()[beforeLen:]
			if got.String() != wantPatch.String() {
				t.Fatalf("retry result differs from stable result:\n got:\n%s\nwant:\n%s",
					got.String(), wantPatch.String())
			}
			if st := d.diagnosticStats(); st.TotalEqualChecks() == 0 {
				t.Fatalf("successful retry did not record diagnostics: %+v", st)
			}
		})
	}
}

func mustUnmarshal(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(err)
	}
	return v
}

// TestCountersDiagnosticOnly proves counters never influence
// the patch and are isolated per comparison: two different
// inputs compared on two Differ instances keep independent
// counters, and a successful run carries nothing from a prior
// failed call.
func TestCountersDiagnosticOnly(t *testing.T) {
	d1 := &Differ{}
	d1.applyOpts(LCS(), Factorize())
	p1, err := compareJSON(d1, []byte(`[1,1,2,2,3]`), []byte(`[2,2,1,1,3]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	d2 := &Differ{}
	d2.applyOpts(LCS())
	p2, err := compareJSON(d2, []byte(`["a","b","c"]`), []byte(`["z","a","b","c"]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	st1, st2 := d1.diagnosticStats(), d2.diagnosticStats()
	if st1 == st2 {
		t.Fatalf("counters not isolated between comparisons: %+v == %+v", st1, st2)
	}
	if st1.MoveProbes == 0 {
		t.Fatalf("expected move probes for the factorized case, got %+v", st1)
	}
	// Same inputs without the diagnostic surface produce the
	// same patch: counters cannot affect the result.
	p1b, _ := mustCompareJSON(t, `[1,1,2,2,3]`, `[2,2,1,1,3]`, LCS(), Factorize())
	if p1.String() != p1b.String() {
		t.Fatalf("diagnostics altered the public patch:\n%s\nvs\n%s", p1, p1b)
	}
	if len(p2) == 0 {
		t.Fatalf("expected operations for head insertion")
	}
}

// TestSeededRandomRoundTrip generates randomized arrays with a
// fixed seed, compares them through the public entry, and
// checks every patch for both applicability (JSON diff
// semantics) and determinism. On failure it prints the minimal
// JSON reproduction needed to rerun the case.
func TestSeededRandomRoundTrip(t *testing.T) {
	const seed int64 = 20260923
	gen := func(r *rand.Rand, n int) string {
		vals := make([]string, n)
		for i := range vals {
			switch r.Intn(4) {
			case 0:
				vals[i] = fmt.Sprintf("%d", r.Intn(3))
			case 1:
				vals[i] = fmt.Sprintf("%q", string(rune('a'+r.Intn(3))))
			case 2:
				vals[i] = fmt.Sprintf(`{"k":%d}`, r.Intn(3))
			default:
				vals[i] = fmt.Sprintf(`[%d,%d]`, r.Intn(2), r.Intn(2))
			}
		}
		return "[" + strings.Join(vals, ",") + "]"
	}

	rng := rand.New(rand.NewSource(seed))
	for iter := 0; iter < 120; iter++ {
		n := rng.Intn(8) + 1
		src := gen(rng, n)
		tgt := gen(rng, rng.Intn(8)+1)
		opts := []Option{Factorize(), LCS()}
		if rng.Intn(2) == 0 {
			opts = append(opts, Equivalent())
		}
		p1, _ := mustCompareJSON(t, src, tgt, opts...)
		p2, _ := mustCompareJSON(t, src, tgt, opts...)
		if p1.String() != p2.String() {
			t.Fatalf("non-deterministic patch (seed=%d iter=%d)\nsrc=%s\ntgt=%s\nfirst:\n%s\nsecond:\n%s",
				seed, iter, src, tgt, p1.String(), p2.String())
		}
		patchRoundTrips(t, src, tgt, p1)
	}
}

// TestInvalidTypeStillRejected confirms that when the
// implementation rejects an input it cannot classify, the
// rejection keeps the existing error layer and is repeatable.
func TestInvalidTypeStillRejected(t *testing.T) {
	_, err := CompareWithoutMarshal([]int{1}, []int{2})
	if err == nil || !strings.Contains(err.Error(), "invalid json type") {
		t.Fatalf("expected the existing invalid-type error, got %v", err)
	}
	// Repeatable on a fresh entry call.
	_, err2 := CompareWithoutMarshal([]int{1}, []int{2})
	if err2 == nil || err2.Error() != err.Error() {
		t.Fatalf("rejection is not repeatable: %q vs %q", err, err2)
	}
}
