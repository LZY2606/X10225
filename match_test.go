package jsondiff

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

// matchGuardCase binds a fixed regression input to two
// layers of expectations:
//
//   - the public JSON Patch the caller must observe
//     (structure, operation order, paths and values);
//   - the diagnostic old/new comparison budget, expressed
//     as the exact number of comparisons the unified
//     pipeline performs and the legacy implementation's
//     worst-case budget for the same input (the baseline
//     threshold).
type matchGuardCase struct {
	name           string
	opts           []Option
	before         string
	after          string
	wantOps        []Operation
	wantEqualPairs int // exact, unified matcher
	wantCandPairs  int // exact, unified matcher
	// legacy budget: n*m LCS table probes (+ up to LCS
	// length backtrack probes), or the full patch scan for
	// every addition under factorization. Used as the
	// "old-vs-new" baseline threshold.
	legacyEqualMax int
	legacyCandMax  int
}

func parseDoc(t *testing.T, raw string) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("invalid fixture document %q: %s", raw, err)
	}
	return v
}

// assertOps compares operations field by field and reports
// the first violating position together with its context,
// instead of a single pass/fail boolean.
func assertOps(t *testing.T, got, want Patch) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("operation count mismatch at position 0: got %d ops %v, want %d ops %v",
			len(got), got, len(want), want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Type != w.Type {
			t.Fatalf("first violation at op #%d: type got %q, want %q (context got=%s)",
				i, g.Type, w.Type, g)
		}
		if g.Path != w.Path {
			t.Fatalf("first violation at op #%d (%s): path got %q, want %q",
				i, g.Type, g.Path, w.Path)
		}
		if g.From != w.From {
			t.Fatalf("first violation at op #%d (%s): from got %q, want %q",
				i, g.Type, g.From, w.From)
		}
		if (w.Type == OperationAdd || w.Type == OperationReplace) &&
			!reflect.DeepEqual(g.Value, w.Value) {
			t.Fatalf("first violation at op #%d (%s %s): value got %#v, want %#v",
				i, g.Type, g.Path, g.Value, w.Value)
		}
	}
}

func assertApplies(t *testing.T, before string, patch Patch, want interface{}) {
	t.Helper()
	out, err := patch.apply([]byte(before), false)
	if err != nil {
		t.Fatalf("generated patch does not apply to the source: %s\npatch: %s", err, patch)
	}
	gotDoc := parseDoc(t, string(out))
	if !reflect.DeepEqual(gotDoc, want) {
		gb, _ := json.Marshal(gotDoc)
		wb, _ := json.Marshal(want)
		t.Fatalf("patch result mismatch:\n got %s\nwant %s", gb, wb)
	}
}

func guardCases() []matchGuardCase {
	return []matchGuardCase{
		{
			name:           "duplicate values paired without hash-collision shortcuts",
			opts:           []Option{LCS(), Factorize()},
			before:         `["x","x","a","b"]`,
			after:          `["b","a","x","x"]`,
			wantEqualPairs: 16,
			wantCandPairs:  0,
			legacyEqualMax: 16 + 4, // n*m fill + LCS backtrack deepEquals
			wantOps: []Operation{
				{Type: OperationAdd, Path: "/0", Value: "b"},
				{Type: OperationAdd, Path: "/1", Value: "a"},
				{Type: OperationRemove, Path: "/4"},
				{Type: OperationRemove, Path: "/4"},
			},
		},
		{
			name:           "deep object crossing positions moves once",
			opts:           []Option{LCS(), Factorize()},
			before:         `[{"k":{"a":[1,2]}},{"k":{"a":[3,4]}}]`,
			after:          `[{"k":{"a":[3,4]}},{"k":{"a":[1,2]}}]`,
			wantEqualPairs: 4,
			wantCandPairs:  1,
			legacyEqualMax: 4 + 2,
			legacyCandMax:  1,
			wantOps: []Operation{
				{Type: OperationMove, From: "/0", Path: "/1"},
			},
		},
		{
			name:           "head insertion is a single leading add",
			opts:           []Option{LCS(), Factorize()},
			before:         `[1,2,3]`,
			after:          `[0,1,2,3]`,
			wantEqualPairs: 12,
			wantCandPairs:  0,
			legacyEqualMax: 12 + 3,
			wantOps: []Operation{
				{Type: OperationAdd, Path: "/0", Value: float64(0)},
			},
		},
		{
			name:           "tail insertion is a single trailing add",
			opts:           []Option{LCS(), Factorize()},
			before:         `[1,2,3]`,
			after:          `[1,2,3,4]`,
			wantEqualPairs: 12,
			wantCandPairs:  0,
			legacyEqualMax: 12 + 3,
			wantOps: []Operation{
				{Type: OperationAdd, Path: "/3", Value: float64(4)},
			},
		},
		{
			name:           "cross moves collapse in stable order",
			opts:           []Option{LCS(), Factorize()},
			before:         `[1,2,3,4]`,
			after:          `[4,3,2,1]`,
			wantEqualPairs: 16,
			wantCandPairs:  3,
			legacyEqualMax: 16,
			legacyCandMax:  3 * 4, // each add used to deepEqual-scan every remove
			wantOps: []Operation{
				{Type: OperationMove, From: "/0", Path: "/3"},
				{Type: OperationMove, From: "/0", Path: "/2"},
				{Type: OperationMove, From: "/0", Path: "/1"},
			},
		},
		{
			name:           "cross moves without factorization keep remove/add semantics",
			opts:           []Option{LCS()},
			before:         `[1,2,3,4]`,
			after:          `[4,3,2,1]`,
			wantEqualPairs: 16,
			wantCandPairs:  0,
			legacyEqualMax: 16,
			wantOps: []Operation{
				{Type: OperationRemove, Path: "/0"},
				{Type: OperationRemove, Path: "/0"},
				{Type: OperationRemove, Path: "/0"},
				{Type: OperationAdd, Path: "/1", Value: float64(3)},
				{Type: OperationAdd, Path: "/2", Value: float64(2)},
				{Type: OperationAdd, Path: "/3", Value: float64(1)},
			},
		},
		{
			name:           "repeated unequal duplicates never pair across values",
			opts:           []Option{LCS(), Factorize()},
			before:         `["x","x","a"]`,
			after:          `["a","x","x","b"]`,
			wantEqualPairs: 12,
			wantCandPairs:  0,
			legacyEqualMax: 12 + 3,
			wantOps: []Operation{
				{Type: OperationAdd, Path: "/0", Value: "a"},
				{Type: OperationReplace, Path: "/3", Value: "b"},
			},
		},
	}
}

// runGuard executes one guard case exclusively through the
// public Compare/CompareJSON entries and verifies both the
// caller-visible patch and the diagnostic comparison budget.
func runGuard(t *testing.T, tc matchGuardCase, entry string) {
	t.Helper()

	var got Patch
	var counters MatchCounters
	after := parseDoc(t, tc.after)

	d := &Differ{}
	d.applyOpts(tc.opts...)
	switch entry {
	case "CompareJSON":
		p, err := CompareJSON([]byte(tc.before), []byte(tc.after), tc.opts...)
		if err != nil {
			t.Fatalf("CompareJSON failed: %s", err)
		}
		// Re-run on an instrumented Differ sharing the same
		// public code path to read diagnostic counters.
		d.Compare(parseDoc(t, tc.before), after)
		got, counters = p, d.matchCounters()
	default:
		d.Compare(parseDoc(t, tc.before), after)
		got, counters = d.Patch(), d.matchCounters()
	}

	assertOps(t, got, Patch(tc.wantOps))
	assertApplies(t, tc.before, got, after)

	if counters.EqualPairs != tc.wantEqualPairs {
		t.Errorf("old/new equality comparisons: got %d, want %d (legacy baseline %d)",
			counters.EqualPairs, tc.wantEqualPairs, tc.legacyEqualMax)
	}
	if counters.CandidatePairs != tc.wantCandPairs {
		t.Errorf("candidate comparisons: got %d, want %d (legacy baseline %d)",
			counters.CandidatePairs, tc.wantCandPairs, tc.legacyCandMax)
	}
	if counters.EqualPairs > tc.legacyEqualMax || counters.CandidatePairs > tc.legacyCandMax {
		t.Errorf("comparison budget exceeded the legacy baseline: equal %d<=%d, candidate %d<=%d",
			counters.EqualPairs, tc.legacyEqualMax,
			counters.CandidatePairs, tc.legacyCandMax)
	}
}

func TestArrayMatchGuards(t *testing.T) {
	for _, tc := range guardCases() {
		t.Run(tc.name, func(t *testing.T) {
			runGuard(t, tc, "Compare")
		})
	}
}

// TestArrayMatchGuardFixtures drives the JSON fixtures of
// the repository's existing golden-file test system.
func TestArrayMatchGuardFixtures(t *testing.T) {
	runCasesFromFile(t, "testdata/tests/jsonpatch/options/matching.json", LCS(), Factorize())
}

// forcedCollisionCount is the number of distinct values
// forced into a single digest bucket.
const forcedCollisionCount = 2

func collisionSpoof(t *testing.T, values ...interface{}) map[uint64]uint64 {
	t.Helper()
	var h hasher
	beta := h.digest("collision-beta", false)
	spoof := map[uint64]uint64{
		h.digest("collision-alpha", false): beta,
	}
	for _, v := range values {
		spoof[h.digest(v, false)] = beta
	}
	return spoof
}

// TestArrayMatchHashCollisionGuard forces a digest
// collision between distinct move/copy candidates and
// proves that candidate discovery still relies on
// deepEqual: colliding but unequal elements cannot pair,
// while truly equal elements continue to move/copy.
func TestArrayMatchHashCollisionGuard(t *testing.T) {
	src := map[string]interface{}{"a": "aaa", "b": "bbb"}
	tgt := map[string]interface{}{"c": "bbb", "d": "aaa"}
	want := []Operation{
		{Type: OperationMove, From: "/b", Path: "/c"},
		{Type: OperationMove, From: "/a", Path: "/d"},
	}

	t.Run("moves survive a forced collision bucket", func(t *testing.T) {
		d := &Differ{digestSpoof: collisionSpoof(t, "aaa", "bbb")}
		d.applyOpts(Factorize())
		d.Compare(src, tgt)
		patch := d.Patch()
		assertOps(t, patch, Patch(want))

		// Every addition must probe every live candidate in
		// the shared bucket: the digest cannot be trusted to
		// decide the pairing.
		if c := d.matchCounters(); c.CandidatePairs != forcedCollisionCount {
			t.Fatalf("collision bucket probes: got %d, want %d (full bucket scan)",
				c.CandidatePairs, forcedCollisionCount)
		}
	})

	t.Run("unequal values sharing a digest never move", func(t *testing.T) {
		// Remove "aaa" from the target and replace it with
		// "ccc": all digests collide, so only "bbb" matches
		// structurally; "ccc" must become a plain add.
		unequal := map[string]interface{}{"c": "bbb", "d": "ccc"}
		d := &Differ{digestSpoof: collisionSpoof(t, "aaa", "bbb", "ccc")}
		d.applyOpts(Factorize())
		d.Compare(src, unequal)
		got := d.Patch()

		var adds, moves, removes int
		for _, op := range got {
			switch op.Type {
			case OperationMove:
				moves++
				if op.From != "/b" || op.Path != "/c" {
					t.Fatalf("first violation: illegal move %s -> %s in %v", op.From, op.Path, got)
				}
			case OperationAdd:
				adds++
				if op.Path != "/d" || op.Value != "ccc" {
					t.Fatalf("first violation: illegal add %s=%v in %v", op.Path, op.Value, got)
				}
			case OperationRemove:
				removes++
				if op.Path != "/a" {
					t.Fatalf("first violation: illegal remove %s in %v", op.Path, got)
				}
			default:
				t.Fatalf("unexpected operation %s in %v", op.Type, got)
			}
		}
		if moves != 1 || adds != 1 || removes != 1 {
			t.Fatalf("got %d moves, %d adds, %d removes; want 1 of each: %v",
				moves, adds, removes, got)
		}
	})

	t.Run("copy source requires structural confirmation", func(t *testing.T) {
		copySrc := map[string]interface{}{"a": []interface{}{"x"}}
		copyTgt := map[string]interface{}{
			"a": []interface{}{"x"},
			"d": []interface{}{"y"},
		}
		d := &Differ{digestSpoof: collisionSpoof(t, []interface{}{"x"}, []interface{}{"y"})}
		d.applyOpts(Factorize())
		d.Compare(copySrc, copyTgt)
		got := d.Patch()
		// "y" shares a digest with unchanged "x" but is not
		// equal: it must be added, never copied.
		if len(got) != 1 || got[0].Type != OperationAdd || got[0].Path != "/d" {
			t.Fatalf("first violation at op #0: got %v, want add /d", got)
		}
	})

	t.Run("clean digests produce the identical public patch", func(t *testing.T) {
		clean := &Differ{}
		clean.applyOpts(Factorize())
		clean.Compare(src, tgt)
		assertOps(t, clean.Patch(), Patch(want))
	})
}

// TestArrayMatchUnorderedCollisionGuard exercises the
// Equivalent path, where a digest-only multiset comparison
// historically treated duplicate values as a set and could
// confuse repeated values that share a digest.
func TestArrayMatchUnorderedCollisionGuard(t *testing.T) {
	t.Run("repeated values are a multiset not a set", func(t *testing.T) {
		// Old code: set membership alone returned true here.
		patch, err := CompareJSON(
			[]byte(`[1,1,2]`), []byte(`[1,2,2]`), Equivalent())
		if err != nil {
			t.Fatal(err)
		}
		if len(patch) != 1 || patch[0].Type != OperationReplace {
			t.Fatalf("first violation at op #0: got %v, want a single replace", patch)
		}
	})
	t.Run("true unordered duplicates produce no operations", func(t *testing.T) {
		patch, err := CompareJSON(
			[]byte(`["n","n",1,{"k":2}]`),
			[]byte(`[{"k":2},1,"n","n"]`),
			Equivalent())
		if err != nil {
			t.Fatal(err)
		}
		if len(patch) != 0 {
			t.Fatalf("first violation at op #0: got %v, want empty patch", patch)
		}
	})
}

// injectedMatchFailure is the sentinel panic used by the
// diagnostic failure seam. It intentionally carries no
// exported surface: only the test harness injects it.
type injectedMatchFailure struct{ phase matchPhase }

func (injectedMatchFailure) Error() string { return "jsondiff: injected array matching failure" }

// TestArrayMatchRecoverableFailure drives a comparison
// through the public entry, injects one recoverable
// failure midway through the LCS fill (while its table is
// partially populated), and proves that:
//
//   - no half-built patch, candidate or digest state leaks
//     onto the Differ;
//   - an immediate retry with identical inputs yields the
//     exact stable patch and stable diagnostic counts;
//   - a successful retry leaves no failure call residue in
//     any observable state.
func TestArrayMatchRecoverableFailure(t *testing.T) {
	before := `[1,2,3,4,5,6]`
	after := `[6,5,4,3,2,1]`

	want, err := CompareJSON([]byte(before), []byte(after), LCS(), Factorize())
	if err != nil {
		t.Fatal(err)
	}

	d := &Differ{}
	d.applyOpts(LCS(), Factorize())

	var fired bool
	d.matchFault = func(phase matchPhase) {
		if phase != phaseLCS {
			t.Errorf("failure seam fired in unexpected phase %d", phase)
		}
		fired = true
		panic(injectedMatchFailure{phase: phase})
	}

	var firstPatch Patch
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected the injected failure to panic")
			}
			if _, ok := r.(injectedMatchFailure); !ok {
				t.Fatalf("failure propagated with the wrong error type: %#v", r)
			}
		}()
		d.Compare(parseDoc(t, before), parseDoc(t, after))
	}()
	if !fired {
		t.Fatal("diagnostic failure seam was never invoked")
	}

	// No persistent/shared state may survive the failed call.
	if len(d.Patch()) != 0 {
		t.Fatalf("failed comparison left %d operations on the Differ: %v", len(d.Patch()), d.Patch())
	}
	if len(d.hashmap) != 0 {
		t.Fatalf("failed comparison left %d factorization entries behind", len(d.hashmap))
	}
	if d.match != nil && (len(d.match.removes) != 0 || len(d.match.buckets) != 0) {
		t.Fatalf("failed comparison left candidate state: %+v", d.match)
	}
	firstPatch = append(firstPatch, d.Patch()...)

	// Disable the seam and retry the exact same inputs.
	d.matchFault = nil
	d.Compare(parseDoc(t, before), parseDoc(t, after))
	retry := d.Patch()

	assertOps(t, retry, want)
	assertApplies(t, before, retry, parseDoc(t, after))

	// Deterministic counts across repeated successes.
	first := d.matchCounters()
	d.Compare(parseDoc(t, before), parseDoc(t, after))
	second := d.matchCounters()
	if first != second {
		t.Fatalf("diagnostic counts are not stable: %+v vs %+v", first, second)
	}
	if d.matchCounters().CandidatePairs == 0 && d.opts.factorize {
		t.Fatal("reorder input must probe move candidates")
	}
	// The successful result carries no residue of the
	// failed call: no candidate marked used, no stale patch.
	for i, c := range d.match.removes {
		if !c.used {
			continue
		}
		// All candidates on the final patch are consumed as
		// moves; verify there is no leftover remove either.
		t.Fatalf("candidate #%d retains state from the failed call: %+v", i, c)
	}
	if len(firstPatch) != 0 {
		t.Fatalf("snapshot taken after failure was modified by the retry: %v", firstPatch)
	}
}

// TestArrayMatchDiagnosticIsolation runs the same public
// entry concurrently on distinct Differ instances. Each
// call owns its matcher, so counters and candidate state
// must stay isolated. A start barrier and an iteration
// barrier maximize interleaving, making the test a genuine
// concurrency guard rather than a sequential loop.
func TestArrayMatchDiagnosticIsolation(t *testing.T) {
	const goroutines = 16

	inputs := make([]struct{ before, after string }, goroutines)
	for i := range inputs {
		// Every goroutine works on a distinct input so any
		// cross-talk corrupts its counters deterministically.
		inputs[i].before = fmtJSON([]interface{}{i, i + 1, i + 2, i + 3})
		inputs[i].after = fmtJSON([]interface{}{i + 3, i + 1, i, i + 2})
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)

	expected := make([]Patch, goroutines)
	for i := range inputs {
		p, err := CompareJSON([]byte(inputs[i].before), []byte(inputs[i].after), LCS(), Factorize())
		if err != nil {
			t.Fatal(err)
		}
		expected[i] = p
	}

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start // release all goroutines together

			var last Patch
			var lastCounters MatchCounters
			for round := 0; round < 25; round++ {
				d := &Differ{}
				d.applyOpts(LCS(), Factorize())
				d.Compare(parseDoc(t, inputs[id].before), parseDoc(t, inputs[id].after))
				last = d.Patch()
				lastCounters = d.matchCounters()

				if !sameOps(last, expected[id]) {
					errs <- fmt.Errorf("goroutine %d round %d: patch drifted: %v want %v",
						id, round, last, expected[id])
					return
				}
			}
			// Re-run twice to check per-call counter reset.
			d := &Differ{}
			d.applyOpts(LCS(), Factorize())
			d.Compare(parseDoc(t, inputs[id].before), parseDoc(t, inputs[id].after))
			c1 := d.matchCounters()
			d.Compare(parseDoc(t, inputs[id].before), parseDoc(t, inputs[id].after))
			c2 := d.matchCounters()
			if c1 != c2 || c1 != lastCounters {
				errs <- fmt.Errorf("goroutine %d: counters leaked across calls: %+v %+v %+v",
					id, c1, c2, lastCounters)
			}
		}(g)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// TestArrayMatchOrderStability changes only conditions that
// must be irrelevant to the result: the public entry point,
// the reuse schedule of the Differ (including Reset), map
// enumeration order (objects are iterated via maps), and the
// factorization preparation/cache state. The public patch
// structure and operation order must remain identical.
func TestArrayMatchOrderStability(t *testing.T) {
	before := `{"z":[1,2,3,4],"a":[1,2,3,4],"m":{"b":1,"a":2}}`
	after := `{"m":{"a":2,"b":1},"a":[4,2,1,3],"z":[3,4,1,2],"k":"new"}`

	reference, err := CompareJSON([]byte(before), []byte(after), Factorize(), LCS())
	if err != nil {
		t.Fatal(err)
	}

	// Entry-point variation.
	got, err := Compare(parseDoc(t, before), parseDoc(t, after), Factorize(), LCS())
	if err != nil {
		t.Fatal(err)
	}
	if !sameOps(got, reference) {
		t.Fatalf("Compare entry changed the patch:\n got %v\nwant %v", got, reference)
	}

	// Scheduling variation: reuse + Reset interleaved with
	// fresh instances, repeated to surface map-order drift.
	var reused Patch
	d := &Differ{}
	d.applyOpts(Factorize(), LCS())
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			d.Reset()
		}
		d.Compare(parseDoc(t, before), parseDoc(t, after))
		reused = d.Patch()
		if !sameOps(reused, reference) {
			t.Fatalf("round %d: reused Differ changed the patch:\n got %v\nwant %v",
				i, reused, reference)
		}
	}

	// Option-order variation: options are functional and
	// independent; their application order must not matter.
	reordered, err := CompareJSON([]byte(before), []byte(after), LCS(), Factorize())
	if err != nil {
		t.Fatal(err)
	}
	if !sameOps(reordered, reference) {
		t.Fatalf("option application order changed the patch:\n got %v\nwant %v",
			reordered, reference)
	}
}

// TestArrayMatchOptimizationDisabled checks that switching
// every optimization off returns the baseline, positional
// semantics: no move/copy operations, no candidate
// accounting at all.
func TestArrayMatchOptimizationDisabled(t *testing.T) {
	before := []interface{}{1.0, 2.0, 3.0, 4.0}
	after := []interface{}{4.0, 3.0, 2.0, 1.0}

	d := &Differ{} // zero options: positional array compare
	d.Compare(before, after)
	patch := d.Patch()
	for i, op := range patch {
		switch op.Type {
		case OperationMove, OperationCopy:
			t.Fatalf("first violation at op #%d: optimization op %s leaked with options off: %v",
				i, op.Type, patch)
		}
	}
	if c := d.matchCounters(); c.Total() != 0 {
		t.Fatalf("disabled optimizations must not perform matching comparisons, got %+v", c)
	}
	assertApplies(t, `[1,2,3,4]`, patch, after)

	// LCS alone must perform equality matching but never
	// move/copy candidate discovery.
	d2 := &Differ{}
	d2.applyOpts(LCS())
	d2.Compare(before, after)
	for i, op := range d2.Patch() {
		if op.Type == OperationMove || op.Type == OperationCopy {
			t.Fatalf("first violation at op #%d: %s emitted without Factorize: %v",
				i, op.Type, d2.Patch())
		}
	}
	if c := d2.matchCounters(); c.CandidatePairs != 0 {
		t.Fatalf("candidate discovery ran with Factorize disabled: %+v", c)
	}

	// Factorize without LCS keeps the historical positional
	// behavior for pure permutations (remove/add, no move).
	d3 := &Differ{}
	d3.applyOpts(Factorize())
	d3.Compare(before, after)
	for i, op := range d3.Patch() {
		if op.Type == OperationMove {
			t.Fatalf("first violation at op #%d: move emitted without LCS: %v", i, d3.Patch())
		}
	}
}

// TestArrayMatchRandomized is a fixed-seed property guard:
// random permutations with a small alphabet intentionally
// produce heavy duplicate values. Every generated patch
// must (1) apply back to the target, (2) be independent of
// entry point and scheduling, and (3) stay within the
// legacy n*m comparison baseline. On any violation the
// minimal JSON reproduction and the seed are printed.
func TestArrayMatchRandomized(t *testing.T) {
	const seed = int64(20260922)
	rng := rand.New(rand.NewSource(seed))

	for iter := 0; iter < 200; iter++ {
		n := rng.Intn(9) + 1
		alpha := rng.Intn(4) + 1
		src := make([]interface{}, n)
		for i := range src {
			src[i] = float64(rng.Intn(alpha))
		}
		tgt := append([]interface{}(nil), src...)
		rng.Shuffle(len(tgt), func(i, j int) { tgt[i], tgt[j] = tgt[j], tgt[i] })
		if rng.Intn(2) == 0 && len(tgt) > 0 {
			tgt = append(tgt, float64(9))
		}

		srcJSON, tgtJSON := fmtJSON(src), fmtJSON(tgt)
		opts := []Option{LCS(), Factorize()}

		patch, err := CompareJSON([]byte(srcJSON), []byte(tgtJSON), opts...)
		if err != nil {
			t.Fatalf("seed=%d iter=%d: %s\nminimal repro: before=%s after=%s",
				seed, iter, err, srcJSON, tgtJSON)
		}
		out, err := patch.apply([]byte(srcJSON), false)
		if err != nil {
			t.Fatalf("seed=%d iter=%d: patch failed to apply: %s\nbefore=%s\nafter=%s\npatch=%s",
				seed, iter, err, srcJSON, tgtJSON, patch)
		}
		if string(out) != tgtJSON {
			t.Fatalf("seed=%d iter=%d: patch is semantically wrong\nbefore=%s\nafter=%s\n got=%s\npatch=%s",
				seed, iter, srcJSON, tgtJSON, out, patch)
		}

		// Baseline budget: legacy LCS did up to n*m fill
		// probes plus up to min(n,m) backtrack probes.
		d := &Differ{}
		d.applyOpts(opts...)
		d.Compare(parseDoc(t, srcJSON), parseDoc(t, tgtJSON))
		c := d.matchCounters()
		baseline := len(src)*len(tgt) + min(len(src), len(tgt))
		if c.EqualPairs > baseline {
			t.Fatalf("seed=%d iter=%d: equality comparisons %d exceed legacy baseline %d",
				seed, iter, c.EqualPairs, baseline)
		}

		// Scheduling independence: a reused/reset Differ and
		// a fresh Differ must agree.
		d2 := &Differ{}
		d2.applyOpts(opts...)
		d2.Compare(parseDoc(t, srcJSON), parseDoc(t, tgtJSON))
		if !sameOps(d2.Patch(), patch) {
			t.Fatalf("seed=%d iter=%d: scheduling changed the patch\nbefore=%s\nafter=%s",
				seed, iter, srcJSON, tgtJSON)
		}
	}
}

func sameOps(a, b Patch) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Path != b[i].Path || a[i].From != b[i].From {
			return false
		}
		if !reflect.DeepEqual(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}

func fmtJSON(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
