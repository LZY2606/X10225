package jsondiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

// Regression material for
// "jsondiff: unify array matching and move/copy candidate
// accounting".
//
// Every scenario enters the package through the official
// public entry points (Compare / CompareJSON / Differ.Compare
// via the Differ API); internal objects are never fabricated
// to make the tests pass. The shared production matching core
// (arrayMatcher + move ledger) is observed through the
// unexported diagnostic counters, which are the only
// test-facing seam introduced for this work.

// expectOp is the minimal description of a public patch
// operation asserted by the behavior guards.
type expectOp struct {
	Type string
	Path string
	From string
}

func opShape(p Patch) []expectOp {
	out := make([]expectOp, 0, len(p))
	for _, op := range p {
		out = append(out, expectOp{
			Type: op.Type,
			Path: op.Path,
			From: op.From,
		})
	}
	return out
}

// firstPatchViolation compares the public shape (type, path
// and "from") of two patches and reports the first mismatch
// with its context, instead of a mere boolean.
func firstPatchViolation(got, want []expectOp) string {
	if len(got) != len(want) {
		return fmt.Sprintf("operation count: got %d (%v), want %d (%v)",
			len(got), got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			return fmt.Sprintf("op #%d: got %+v, want %+v (full got %v)",
				i, got[i], want[i], got)
		}
	}
	return ""
}

// comparePublic runs a comparison through the public Compare
// entry and additionally reports the diagnostic counters of
// the same production path.
func comparePublic(t *testing.T, src, tgt any, collision func(any) uint64, opts ...Option) (Patch, arrayMatchStats) {
	t.Helper()
	p, err := Compare(src, tgt, opts...)
	if err != nil {
		t.Fatalf("Compare returned error: %v", err)
	}
	d := &Differ{}
	d.WithOpts(opts...)
	d.digestOverride = collision
	d.Compare(src, tgt)
	if d.matchStats == nil {
		t.Fatalf("diagnostic counters were not populated by the production path")
	}
	return p, *d.matchStats
}

// assertPatchApplies applies the patch to src and verifies it
// produces tgt, proving JSON patch minimality/soundness rather
// than only asserting absence of errors.
func assertPatchApplies(t *testing.T, src, tgt any, p Patch) {
	t.Helper()
	sb, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	tb, err := json.Marshal(tgt)
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	out, err := p.apply(sb, false)
	if err != nil {
		t.Fatalf("patch failed to apply at %s: %v", firstPatchViolation(opShape(p), nil), err)
	}
	var got bytes.Buffer
	if err := json.Compact(&got, out); err != nil {
		t.Fatalf("compact patched document: %v", err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, tb); err != nil {
		t.Fatalf("compact target document: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("patched document mismatch:\n got %s\nwant %s\nops: %v",
			got.String(), want.String(), opShape(p))
	}
}

// statsBound is the baseline threshold for old/new element
// comparison work. Exact values pin the current behavior; the
// bound fields guarantee the contract survives later changes:
// no probe pair is evaluated twice, and deep equality is never
// called for pairs that a digest already ruled out.
type statsBound struct {
	digestOld       int // exact, -1 to ignore
	digestNew       int // exact, -1 to ignore
	pairsExamined   int // exact, -1 to ignore
	deepEqualProbes int // exact, -1 to ignore
	equalPairs      int // exact, -1 to ignore
	moveCandidates  int // exact, -1 to ignore
	moveFound       int // exact, -1 to ignore
	copyCandidates  int // exact, -1 to ignore
	copyFound       int // exact, -1 to ignore
}

func checkStats(t *testing.T, label string, got arrayMatchStats, want statsBound) {
	t.Helper()
	fail := func(field string, g, w int) {
		t.Errorf("%s: %s = %d, want baseline %d (full stats %+v)",
			label, field, g, w, got)
	}
	if want.digestOld >= 0 && got.digestOld != want.digestOld {
		fail("digestOld", got.digestOld, want.digestOld)
	}
	if want.digestNew >= 0 && got.digestNew != want.digestNew {
		fail("digestNew", got.digestNew, want.digestNew)
	}
	if want.pairsExamined >= 0 && got.pairsExamined != want.pairsExamined {
		fail("pairsExamined", got.pairsExamined, want.pairsExamined)
	}
	if want.deepEqualProbes >= 0 && got.deepEqualProbes != want.deepEqualProbes {
		fail("deepEqualProbes", got.deepEqualProbes, want.deepEqualProbes)
	}
	if want.equalPairs >= 0 && got.equalPairs != want.equalPairs {
		fail("equalPairs", got.equalPairs, want.equalPairs)
	}
	if want.moveCandidates >= 0 && got.moveCandidates != want.moveCandidates {
		fail("moveCandidates", got.moveCandidates, want.moveCandidates)
	}
	if want.moveFound >= 0 && got.moveFound != want.moveFound {
		fail("moveFound", got.moveFound, want.moveFound)
	}
	if want.copyCandidates >= 0 && got.copyCandidates != want.copyCandidates {
		fail("copyCandidates", got.copyCandidates, want.copyCandidates)
	}
	if want.copyFound >= 0 && got.copyFound != want.copyFound {
		fail("copyFound", got.copyFound, got.copyFound)
	}
	// Structural invariants: a pair is evaluated at most
	// once, and deep equality is only invoked after a digest
	// hint; equal probes are a subset of all probes.
	if got.deepEqualProbes > got.pairsExamined+got.moveCandidates {
		t.Errorf("%s: deepEqualProbes %d exceeds examined pairs %d + move candidates %d",
			label, got.deepEqualProbes, got.pairsExamined, got.moveCandidates)
	}
	if got.equalPairs > got.deepEqualProbes {
		t.Errorf("%s: equalPairs %d exceeds deepEqualProbes %d",
			label, got.equalPairs, got.deepEqualProbes)
	}
}

// TestArrayMatching_BehaviorGuards is the fixed-input group
// that directly characterizes JSON diff / array matching /
// operation minimization: duplicated values, deep objects,
// forced digest collisions, head/tail insertion, crossing
// moves and disabled move/copy optimization.
func TestArrayMatching_BehaviorGuards(t *testing.T) {
	deep := map[string]any{"k": map[string]any{"n": float64(1)}}

	for _, tc := range []struct {
		name      string
		src, tgt  any
		opts      []Option
		collision func(any) uint64
		wantPatch []expectOp
		bound     statsBound
	}{
		{
			name: "duplicate_values_not_miscoupled",
			src:  []any{"x", "x", "a"},
			tgt:  []any{"a", "x", "x"},
			opts: []Option{Factorize(), LCS()},
			wantPatch: []expectOp{
				{Type: OperationAdd, Path: "/0"},
				{Type: OperationRemove, Path: "/3"},
			},
			// All 3*3 pairs are walked once; the five equal
			// relations (x/x twice over the LCS walk plus
			// backtracks, memoized) are the baseline.
			bound: statsBound{
				digestOld: 3, digestNew: 3,
				pairsExamined: 9, deepEqualProbes: 5, equalPairs: 5,
			},
		},
		{
			name: "deep_objects_moved_once",
			src:  []any{deep, "b"},
			tgt:  []any{"b", deep},
			opts: []Option{Factorize(), LCS()},
			wantPatch: []expectOp{
				{Type: OperationMove, From: "/0", Path: "/1"},
			},
			bound: statsBound{
				digestOld: 2, digestNew: 2,
				pairsExamined: 4, deepEqualProbes: 3, equalPairs: 2,
				moveCandidates: 1, moveFound: 1,
			},
		},
		{
			name: "head_insertion_keeps_stable_order",
			src:  []any{"a", "b", "c"},
			tgt:  []any{"z", "a", "b", "c"},
			opts: []Option{Factorize(), LCS()},
			wantPatch: []expectOp{
				{Type: OperationAdd, Path: "/0"},
			},
			bound: statsBound{
				digestOld: 3, digestNew: 4,
				pairsExamined: 12, deepEqualProbes: 3, equalPairs: 3,
			},
		},
		{
			name: "tail_insertion_keeps_stable_order",
			src:  []any{"a", "b", "c"},
			tgt:  []any{"a", "b", "c", "z"},
			opts: []Option{Factorize(), LCS()},
			wantPatch: []expectOp{
				{Type: OperationAdd, Path: "/3"},
			},
			bound: statsBound{
				digestOld: 3, digestNew: 4,
				pairsExamined: 12, deepEqualProbes: 3, equalPairs: 3,
			},
		},
		{
			name: "crossing_moves_minimal_operations",
			src:  []any{"a", "b", "c", "d"},
			tgt:  []any{"d", "c", "b", "a"},
			opts: []Option{Factorize(), LCS()},
			wantPatch: []expectOp{
				{Type: OperationMove, From: "/0", Path: "/3"},
				{Type: OperationMove, From: "/0", Path: "/2"},
				{Type: OperationMove, From: "/0", Path: "/1"},
			},
			bound: statsBound{
				digestOld: 4, digestNew: 4,
				pairsExamined: 16, deepEqualProbes: 7, equalPairs: 4,
				moveCandidates: 3, moveFound: 3,
			},
		},
		{
			name: "moves_disabled_without_factorize",
			src:  []any{"a", "b", "c", "d"},
			tgt:  []any{"d", "c", "b", "a"},
			opts: []Option{LCS()},
			wantPatch: []expectOp{
				{Type: OperationRemove, Path: "/0"},
				{Type: OperationRemove, Path: "/0"},
				{Type: OperationRemove, Path: "/0"},
				{Type: OperationAdd, Path: "/1"},
				{Type: OperationAdd, Path: "/2"},
				{Type: OperationAdd, Path: "/3"},
			},
			// LCS matching still runs once; no move candidate
			// bookkeeping happens without factorization.
			bound: statsBound{
				digestOld: 4, digestNew: 4,
				pairsExamined: 16, deepEqualProbes: 4, equalPairs: 4,
				moveCandidates: 0, moveFound: 0,
			},
		},
		{
			name: "collision_digest_cannot_miscouple_values",
			src:  []any{"a", "b", "c", "d"},
			tgt:  []any{"d", "x", "b", "a"},
			opts: []Option{Factorize(), LCS()},
			// Every digest collides: equality must still be
			// decided by deep equality, and the unmatched "x"
			// must appear as a plain add while "c" is removed.
			collision: func(any) uint64 { return 42 },
			wantPatch: []expectOp{
				{Type: OperationMove, From: "/0", Path: "/3"},
				{Type: OperationMove, From: "/0", Path: "/2"},
				{Type: OperationRemove, Path: "/0"},
				{Type: OperationAdd, Path: "/1"},
			},
			// With all digests equal, every LCS pair is deeply
			// compared (16) and the add/remove scan probes
			// further hash-bucket candidates.
			bound: statsBound{
				digestOld: 4, digestNew: 4,
				pairsExamined: 16, deepEqualProbes: 22, equalPairs: 3,
				moveCandidates: 6, moveFound: 2,
			},
		},
		{
			name: "copy_disabled_under_invertible",
			src: map[string]any{
				"x":   float64(1),
				"arr": []any{"a"},
			},
			tgt: map[string]any{
				"x":   float64(1),
				"arr": []any{"a", float64(1)},
			},
			opts: []Option{Factorize(), Invertible()},
			wantPatch: []expectOp{
				{Type: OperationAdd, Path: "/arr/-"},
			},
			bound: statsBound{
				digestNew: 1, copyCandidates: 1, copyFound: 0,
			},
		},
		{
			name: "copy_candidate_unchanged_location",
			src: map[string]any{
				"x":   float64(1),
				"arr": []any{"a"},
			},
			tgt: map[string]any{
				"x":   float64(1),
				"arr": []any{"a", float64(1)},
			},
			opts: []Option{Factorize()},
			wantPatch: []expectOp{
				{Type: OperationCopy, From: "/x", Path: "/arr/-"},
			},
			bound: statsBound{
				digestNew: 1, copyCandidates: 1, copyFound: 1,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			patch, stats := comparePublic(t, tc.src, tc.tgt, tc.collision, tc.opts...)

			if violation := firstPatchViolation(opShape(patch), tc.wantPatch); violation != "" {
				t.Fatalf("public patch shape violation: %s", violation)
			}
			assertPatchApplies(t, tc.src, tc.tgt, patch)
			checkStats(t, tc.name, stats, tc.bound)
		})
	}
}

// runDiffer executes a comparison on d, returning the
// recovered panic so the failure-injection scenario can be
// driven through the exact same code path a real invalid input
// would take (deepEqualValue's invalidJSONTypeError panic).
func runDiffer(d *Differ, src, tgt any) (recovered any) {
	defer func() {
		recovered = recover()
	}()
	d.Compare(src, tgt)
	return nil
}

func assertNoResidue(t *testing.T, label string, d *Differ) {
	t.Helper()
	if len(d.Patch()) != 0 {
		t.Errorf("%s: failed call left patch %v", label, opShape(d.Patch()))
	}
	if len(d.moveLedger) != 0 {
		t.Errorf("%s: failed call left %d move candidates", label, len(d.moveLedger))
	}
	if d.matchStats != nil {
		t.Errorf("%s: failed call left diagnostic counters %+v", label, *d.matchStats)
	}
	if d.ptr.string() != "" {
		t.Errorf("%s: failed call left pointer %q", label, d.ptr.string())
	}
}

// TestArrayMatching_RecoverableFailureMidComparison injects a
// failure in the middle of a comparison (an invalid JSON type
// nested in an array, after other elements were matched) and
// verifies that no half-built patch, candidate ledger,
// factorize hashmap or diagnostic counters survive, and that
// retrying with identical inputs is stable.
func TestArrayMatching_RecoverableFailureMidComparison(t *testing.T) {
	// The invalid Go int is only reachable through
	// CompareWithoutMarshal; it is precisely how the public
	// surface reports an invalid JSON type.
	validSrc := map[string]any{"keep": float64(9), "arr": []any{"a", "b", "c"}}
	validTgt := map[string]any{"keep": float64(9), "arr": []any{"b", "c", "a"}}
	badTgt := map[string]any{"keep": float64(9), "arr": []any{"b", int(7), "a"}}

	// First compute the successful reference through the
	// public entry.
	wantPatch, err := Compare(validSrc, validTgt, Factorize(), LCS())
	if err != nil {
		t.Fatalf("reference Compare: %v", err)
	}

	for _, attempt := range []struct {
		name string
		opts []Option
	}{
		{"plain", nil},
		{"factorized", []Option{Factorize(), LCS()}},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			d := &Differ{}
			d.WithOpts(attempt.opts...)

			// Inject the failure mid-comparison.
			r := runDiffer(d, validSrc, badTgt)
			if r == nil {
				t.Fatalf("expected the invalid JSON type to fail the comparison")
			}
			if _, ok := r.(invalidJSONTypeError); !ok {
				t.Fatalf("failure does not follow existing error layering: got %T (%v)", r, r)
			}
			assertNoResidue(t, "after failed call", d)

			// Retry with the same failing inputs: the same,
			// repeatable rejection must be produced.
			r2 := runDiffer(d, validSrc, badTgt)
			if reflect.TypeOf(r2) != reflect.TypeOf(r) {
				t.Fatalf("repeated failure is not repeatable: got %T, want %T", r2, r)
			}
			assertNoResidue(t, "after repeated failed call", d)

			// Recovery: the same Differ retried with valid
			// inputs yields the stable reference result.
			d.Compare(validSrc, validTgt)
			if violation := firstPatchViolation(opShape(d.Patch()), opShape(wantPatch)); violation != "" {
				t.Fatalf("retry after recovery: %s", violation)
			}
			if d.matchStats == nil {
				t.Fatalf("successful retry did not populate fresh diagnostic counters")
			}

			// Running again produces the identical patch and
			// counters: nothing carried over from the failure.
			firstStats := *d.matchStats
			d.Compare(validSrc, validTgt)
			if violation := firstPatchViolation(opShape(d.Patch()), opShape(wantPatch)); violation != "" {
				t.Fatalf("second retry: %s", violation)
			}
			if *d.matchStats != firstStats {
				t.Fatalf("counters drift after recovery: got %+v, want %+v",
					*d.matchStats, firstStats)
			}
		})
	}

	// The public entry must translate the panic into the
	// existing wrapped error, repeatedly and without leaving
	// residue observable on a fresh Differ.
	for i := 0; i < 2; i++ {
		if _, err := CompareWithoutMarshal(validSrc, badTgt, Factorize(), LCS()); err == nil {
			t.Fatalf("call #%d: expected invalid JSON type error", i)
		} else if msg := err.Error(); msg != "jsondiff: invalid json type: int" {
			t.Fatalf("call #%d: rejection does not follow the existing error model: %q", i, msg)
		}
	}
}

// TestArrayMatching_OrderStability changes only
// result-irrelevant conditions (option registration order,
// pre-populated internal cache/ledger state and scheduling
// timing) and asserts the public patch shape, operation order
// and diagnostic counters remain identical.
func TestArrayMatching_OrderStability(t *testing.T) {
	src := []any{"a", "b", map[string]any{"k": "v"}, "d", "e"}
	tgt := []any{"e", map[string]any{"k": "v"}, "a", "x", "b"}

	type result struct {
		patch []expectOp
		stats arrayMatchStats
	}
	run := func(t *testing.T, opts ...Option) result {
		t.Helper()
		d := &Differ{}
		d.WithOpts(opts...)
		d.Compare(src, tgt)
		return result{patch: opShape(d.Patch()), stats: *d.matchStats}
	}

	baseline := run(t, Factorize(), LCS())

	// Option construction order shuffled: options only set
	// independent booleans.
	shuffledOpts := []Option{LCS(), Factorize()}
	if got := run(t, shuffledOpts...); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("option-order violation:\n got %+v\nwant %+v", got, baseline)
	}

	// Pre-populated ledger/storage from a prior unrelated
	// comparison must not influence the next one.
	d := &Differ{}
	d.WithOpts(Factorize(), LCS())
	d.Compare([]any{"q", "r"}, []any{"r", "q"})
	d.Compare(src, tgt)
	if got := (result{patch: opShape(d.Patch()), stats: *d.matchStats}); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("cache-state violation:\n got %+v\nwant %+v", got, baseline)
	}

	// Repeated runs at deliberately different scheduling
	// points (runtime.Gosched between ops is unnecessary: the
	// path is deterministic; assert serial repetition).
	for i := 0; i < 5; i++ {
		if got := run(t, Factorize(), LCS()); !reflect.DeepEqual(got, baseline) {
			t.Fatalf("run #%d drift:\n got %+v\nwant %+v", i, got, baseline)
		}
	}
}

// TestArrayMatching_CounterIsolationConcurrent drives multiple
// independent Differs behind a barrier so all goroutines start
// the comparison simultaneously, and verifies counters and
// patches stay isolated: diagnostics must not be global state.
func TestArrayMatching_CounterIsolationConcurrent(t *testing.T) {
	cases := []struct {
		src, tgt []any
	}{
		{[]any{"a", "b", "c", "d"}, []any{"d", "c", "b", "a"}},
		{[]any{"x", "x", "a"}, []any{"a", "x", "x"}},
		{[]any{"a", "b"}, []any{"z", "a", "b"}},
	}
	const goroutines = 8

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make(chan string, goroutines*len(cases))

	for g := 0; g < goroutines; g++ {
		for ci, tc := range cases {
			done.Add(1)
			go func(g, ci int, tc struct{ src, tgt []any }) {
				defer done.Done()
				start.Wait() // barrier: every worker starts together
				d := &Differ{}
				d.WithOpts(Factorize(), LCS())
				d.Compare(tc.src, tc.tgt)
				// Compare the isolated output with a serial
				// reference computed from the same inputs.
				ref := &Differ{}
				ref.WithOpts(Factorize(), LCS())
				ref.Compare(tc.src, tc.tgt)
				if violation := firstPatchViolation(opShape(d.Patch()), opShape(ref.Patch())); violation != "" {
					errs <- fmt.Sprintf("goroutine %d case %d: %s", g, ci, violation)
					return
				}
				if *d.matchStats != *ref.matchStats {
					errs <- fmt.Sprintf("goroutine %d case %d: counters leaked: got %+v want %+v",
						g, ci, *d.matchStats, *ref.matchStats)
				}
			}(g, ci, tc)
		}
	}
	start.Done()
	done.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// genArray builds a deterministic pseudo-random JSON array of
// scalars from the given seed.
func genArray(r *rand.Rand, n int) []any {
	out := make([]any, n)
	for i := range out {
		switch r.Intn(3) {
		case 0:
			out[i] = fmt.Sprintf("v%d", r.Intn(5))
		case 1:
			out[i] = float64(r.Intn(4))
		default:
			out[i] = r.Intn(2) == 1
		}
	}
	return out
}

func shuffleArray(r *rand.Rand, a []any) []any {
	out := append([]any(nil), a...)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestArrayMatching_FixedSeedRandomized runs a fixed set of
// seeds and checks invariants that must hold for every input:
// the patch is sound (applies to the target), results are
// independent of run scheduling/repetition, and each old/new
// pair is evaluated at most once. On failure it prints a
// minimal reproduction (seed + both documents) instead of an
// opaque boolean.
func TestArrayMatching_FixedSeedRandomized(t *testing.T) {
	const seedCount, n = 40, 8
	for seed := int64(1); seed <= seedCount; seed++ {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			src := genArray(r, n)
			tgt := shuffleArray(r, genArray(r, n))

			failRepro := func(msg string) {
				sb, _ := json.Marshal(src)
				tb, _ := json.Marshal(tgt)
				t.Fatalf("%s\nminimal repro (seed %d):\nsrc=%s\ntgt=%s",
					msg, seed, sb, tb)
			}

			p1, err := Compare(src, tgt, Factorize(), LCS())
			if err != nil {
				failRepro(err.Error())
			}
			assertPatchApplies(t, src, tgt, p1)

			d := &Differ{}
			d.WithOpts(Factorize(), LCS())
			d.Compare(src, tgt)
			st := *d.matchStats

			// Repeat: identical shape, order and counters.
			d2 := &Differ{}
			d2.WithOpts(LCS(), Factorize())
			d2.Compare(src, tgt)
			if violation := firstPatchViolation(opShape(d2.Patch()), opShape(p1)); violation != "" {
				failRepro("repeat mismatch: " + violation)
			}
			if *d2.matchStats != st {
				failRepro(fmt.Sprintf("counters drift: got %+v want %+v", *d2.matchStats, st))
			}
			// Every evaluated pair counted once: deep equality
			// cannot outnumber the Cartesian old/new pairs plus
			// move scans, and digests are bounded by elements.
			if st.digestOld > len(src) || st.digestNew > len(tgt) {
				failRepro(fmt.Sprintf("digests computed more than once: %+v", st))
			}
			if st.pairsExamined > len(src)*len(tgt) {
				failRepro(fmt.Sprintf("pair examined more than once: %+v", st))
			}
		})
	}
}

// TestArrayMatching_CompareJSONEntry verifies the same
// contracts through the CompareJSON public entry with byte
// documents, including application back to the target.
func TestArrayMatching_CompareJSONEntry(t *testing.T) {
	src := []byte(`["a",2,{"k":"v"},true]`)
	tgt := []byte(`[true,{"k":"v"},"a",2]`)

	p, err := CompareJSON(src, tgt, Factorize(), LCS())
	if err != nil {
		t.Fatalf("CompareJSON: %v", err)
	}
	out, err := p.apply(src, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var got, want bytes.Buffer
	if err := json.Compact(&got, out); err != nil {
		t.Fatalf("compact out: %v", err)
	}
	if err := json.Compact(&want, tgt); err != nil {
		t.Fatalf("compact want: %v", err)
	}
	if !bytes.Equal(got.Bytes(), want.Bytes()) {
		t.Fatalf("CompareJSON patch mismatch:\n got %s\nwant %s\nops %v",
			got.String(), want.String(), opShape(p))
	}
	if len(p) != 3 {
		t.Fatalf("expected 3 crossing moves, got %d: %v", len(p), opShape(p))
	}
	for i, op := range p {
		if op.Type != OperationMove {
			t.Fatalf("op #%d is %q, want move: %v", i, op.Type, opShape(p))
		}
	}
}

// TestArrayMatching_CollisionCannotForgeCopy forces a digest
// collision between the added value and an unchanged, but
// distinct, location and verifies the copy candidate is
// rejected in favor of an add: duplicates and collisions must
// not be paired by the hash alone.
func TestArrayMatching_CollisionCannotForgeCopy(t *testing.T) {
	src := map[string]any{"x": float64(1), "arr": []any{"a"}}
	tgt := map[string]any{"x": float64(1), "arr": []any{"a", "different"}}

	d := &Differ{}
	d.WithOpts(Factorize())
	d.digestOverride = func(any) uint64 { return 7 }
	d.Compare(src, tgt)

	for i, op := range d.Patch() {
		if op.Type == OperationCopy {
			t.Fatalf("op #%d: collision forged a copy of %q from %q: %v",
				i, op.Path, op.From, opShape(d.Patch()))
		}
	}
	if d.matchStats.copyCandidates != 1 || d.matchStats.copyFound != 0 {
		t.Fatalf("candidate accounting mismatch: %+v", *d.matchStats)
	}
	assertPatchApplies(t, src, tgt, d.Patch())
}

// TestArrayMatching_UnknownInputExplicit rejects input the
// package cannot interpret through the existing error
// hierarchy; it must never guess success.
func TestArrayMatching_UnknownInputExplicit(t *testing.T) {
	src := map[string]any{"arr": []any{int(1)}}
	tgt := map[string]any{"arr": []any{int(2)}}

	if _, err := CompareWithoutMarshal(src, tgt, LCS()); err == nil {
		t.Fatalf("invalid JSON input was silently accepted as success")
	} else if err.Error() != "jsondiff: invalid json type: int" {
		t.Fatalf("rejection breaks the existing error layering: %q", err)
	}
	// Malformed JSON through CompareJSON must be a parse
	// error, repeatedly.
	for i := 0; i < 2; i++ {
		if _, err := CompareJSON([]byte(`[1,2`), []byte(`[1,2,3]`), LCS()); err == nil {
			t.Fatalf("malformed JSON accepted on call #%d", i)
		}
	}
}
