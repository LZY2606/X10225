package jsondiff

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"testing"
)

// This file contains the regression material for the
// contract named "jsondiff: unified array matching and
// move/copy candidate accounting":
//
//   - equality, hashing, LCS and move/copy discovery use
//     a single Differ-owned accounting path;
//   - move/copy candidates are hashed once;
//   - the work performed is exposed as diagnostic counters
//     (Stats), which never influence the patch and are
//     isolated per Differ instance;
//   - hash collisions never pair distinct values;
//   - disabling the optimizations falls back to the
//     original add/remove/replace semantics;
//   - a recoverable mid-comparison failure leaves no
//     cache, counter or temporary state behind.

type arrayMatchCase struct {
	name      string
	src       string
	tgt       string
	opts      []Option
	wantPatch string
	wantStats Stats
}

// Fixed inputs that exercise the guarded behaviors:
// duplicate values, deep objects, colliding hashes,
// head/tail insertions, cross moves and disabled
// move/copy optimizations.
var arrayMatchCases = []arrayMatchCase{
	{
		name: "duplicate-values",
		src:  `["a","b","a","c","a"]`,
		tgt:  `["a","a","c","a","b"]`,
		opts: []Option{Factorize(), LCS()},
		// The single unambiguous move must pair the first
		// removed occurrence, not an arbitrary duplicate.
		wantPatch: `[{"op":"move","from":"/1","path":"/4"}]`,
		wantStats: Stats{Equalities: 39, Hashes: 3, Collisions: 0},
	},
	{
		name: "deep-objects",
		src:  `{"a":[{"id":1,"x":[1,2]},{"id":2}],"b":{}}`,
		tgt:  `{"a":[{"id":2}],"b":{"moved":{"id":1,"x":[1,2]}}}`,
		opts: []Option{Factorize(), LCS()},
		// The deep object is moved once, not replaced
		// element by element.
		wantPatch: `[{"op":"move","from":"/a/0","path":"/b/moved"}]`,
		wantStats: Stats{Equalities: 12, Hashes: 2, Collisions: 0},
	},
	{
		name: "hash-collision-copy",
		// digest(false) == digest(null): both hash the
		// byte '0'. The unchanged "false" value MUST NOT
		// be copied as "null".
		src:       `{"a":false,"b":true}`,
		tgt:       `{"a":false,"b":true,"c":null}`,
		opts:      []Option{Factorize()},
		wantPatch: `[{"value":null,"op":"add","path":"/c"}]`,
		wantStats: Stats{Equalities: 7, Hashes: 3, Collisions: 1},
	},
	{
		name: "hash-collision-move",
		// digest(false) == digest(null), with a duplicate
		// removal on the same value. The removed "false"
		// MUST NOT be moved as "null".
		src:       `{"a":[false,false],"b":[]}`,
		tgt:       `{"a":[false],"b":[null]}`,
		opts:      []Option{Factorize()},
		wantPatch: `[{"op":"remove","path":"/a/1"},{"value":null,"op":"add","path":"/b/-"}]`,
		wantStats: Stats{Equalities: 10, Hashes: 3, Collisions: 2},
	},
	{
		name:      "head-insertion",
		src:       `[1,2,3]`,
		tgt:       `[0,1,2,3,4]`,
		opts:      []Option{LCS()},
		wantPatch: `[{"value":0,"op":"add","path":"/0"},{"value":4,"op":"add","path":"/4"}]`,
		wantStats: Stats{Equalities: 20, Hashes: 0, Collisions: 0},
	},
	{
		name:      "tail-insertion",
		src:       `[0,1,2,3,4]`,
		tgt:       `[1,2,3]`,
		opts:      []Option{LCS()},
		wantPatch: `[{"op":"remove","path":"/0"},{"op":"remove","path":"/3"}]`,
		wantStats: Stats{Equalities: 20, Hashes: 0, Collisions: 0},
	},
	{
		name: "cross-moves",
		src:  `{"a":[1,2],"b":[3,4]}`,
		tgt:  `{"a":[2,1],"b":[4,3]}`,
		opts: []Option{Factorize(), LCS()},
		// Swapped elements become two moves, ordered by
		// stable path enumeration.
		wantPatch: `[{"op":"move","from":"/a/0","path":"/a/1"},{"op":"move","from":"/b/0","path":"/b/1"}]`,
		wantStats: Stats{Equalities: 24, Hashes: 4, Collisions: 0},
	},
	{
		name:      "cross-moves-without-factorize",
		src:       `{"a":[1,2],"b":[3,4]}`,
		tgt:       `{"a":[2,1],"b":[4,3]}`,
		opts:      nil,
		wantPatch: `[{"value":2,"op":"replace","path":"/a/0"},{"value":1,"op":"replace","path":"/a/1"},{"value":4,"op":"replace","path":"/b/0"},{"value":3,"op":"replace","path":"/b/1"}]`,
		wantStats: Stats{Equalities: 11, Hashes: 0, Collisions: 0},
	},
	{
		name:      "copy-disabled-by-invertible",
		src:       `{"a":{"x":1},"b":{}}`,
		tgt:       `{"a":{"x":1},"b":{"c":{"x":1}}}`,
		opts:      []Option{Factorize(), Invertible()},
		wantPatch: `[{"value":{"x":1},"op":"add","path":"/b/c"}]`,
		wantStats: Stats{Equalities: 7, Hashes: 2, Collisions: 0},
	},
	{
		name:      "copy-without-factorize",
		src:       `{"a":{"x":1},"b":{}}`,
		tgt:       `{"a":{"x":1},"b":{"c":{"x":1}}}`,
		opts:      nil,
		wantPatch: `[{"value":{"x":1},"op":"add","path":"/b/c"}]`,
		wantStats: Stats{Equalities: 3, Hashes: 0, Collisions: 0},
	},
}

func runArrayMatchCase(t *testing.T, tc arrayMatchCase) (Patch, Stats) {
	t.Helper()
	d := &Differ{}
	d.WithOpts(tc.opts...)
	d.Compare(mustJSONValue(t, tc.src), mustJSONValue(t, tc.tgt))
	return d.Patch(), d.Stats()
}

// TestArrayMatchGuards checks both what the caller sees
// (patch content and stable order) and the internal
// invariants: the patch applies back to the target, the
// diagnostic counters are exactly as budgeted, and no
// move/copy operation pairs distinct values.
func TestArrayMatchGuards(t *testing.T) {
	for _, tc := range arrayMatchCases {
		t.Run(tc.name, func(t *testing.T) {
			patch, stats := runArrayMatchCase(t, tc)

			assertPatchJSON(t, patch, tc.wantPatch)
			if stats != tc.wantStats {
				t.Errorf("stats mismatch:\n got: %+v\nwant: %+v", stats, tc.wantStats)
			}
			assertAppliesTo(t, patch, tc.src, tc.tgt)
			assertMoveCopySources(t, patch, tc.src)
		})
	}
}

// TestArrayMatchComparisonBudget fixes the old and new
// comparison-count thresholds for a workload of 24 key
// renames. The old candidate discovery linearly scanned
// the patch with deep-equality comparisons: every addition
// was compared against every live removal, i.e. n*n
// comparisons (plus two root-level comparisons). The new
// discovery compares once per registered candidate.
func TestArrayMatchComparisonBudget(t *testing.T) {
	const n = 24

	src := makeObject(func(k int) string { return fmt.Sprintf("a%02d", k) })
	tgt := makeObject(func(k int) string { return fmt.Sprintf("b%02d", k) })

	d := &Differ{}
	d.WithOpts(Factorize())
	d.Compare(src, tgt)
	patch, stats := d.Patch(), d.Stats()

	if len(patch) != n {
		t.Fatalf("expected %d operations, got %d: %s", n, len(patch), patch)
	}
	for i, op := range patch {
		if op.Type != OperationMove {
			t.Errorf("op %d: expected move, got %q", i, op.Type)
		}
	}

	// Old linear-scan budget vs new hash-indexed budget.
	oldBudget := n*n + 2
	newThreshold := 2*n + 4

	if stats.Equalities > newThreshold {
		t.Errorf("equalities %d exceed new threshold %d", stats.Equalities, newThreshold)
	}
	if stats.Equalities*10 >= oldBudget {
		t.Errorf("equalities %d are not at least 10x below the old budget %d",
			stats.Equalities, oldBudget)
	}
	if stats.Hashes != 2*n {
		t.Errorf("hashes: got %d, want %d (one per removal and one per addition)",
			stats.Hashes, 2*n)
	}
	t.Logf("comparison counts: equalities=%d (old=%d), hashes=%d, collisions=%d",
		stats.Equalities, oldBudget, stats.Hashes, stats.Collisions)

	srcJSON, _ := json.Marshal(src)
	assertAppliesTo(t, patch, string(srcJSON), string(mustMarshal(t, tgt)))

	// Counters are per-call: a subsequent, smaller
	// comparison on the same Differ must report only the
	// work of that comparison, without an explicit Reset.
	smallSrc := []interface{}{float64(1)}
	smallTgt := []interface{}{float64(2)}
	d.Compare(smallSrc, smallTgt)

	fresh := &Differ{}
	fresh.WithOpts(Factorize())
	fresh.Compare(smallSrc, smallTgt)
	if d.Stats() != fresh.Stats() {
		t.Errorf("per-call stats mismatch:\n got: %+v\nwant: %+v", d.Stats(), fresh.Stats())
	}
}

func makeObject(key func(int) string) map[string]interface{} {
	const n = 24
	m := make(map[string]interface{}, n)
	for i := 0; i < n; i++ {
		m[key(i)] = fmt.Sprintf("v%02d", i)
	}
	return m
}

// TestArrayMatchRecoverableFailure injects a recoverable
// failure mid-comparison (an invalid JSON value), and
// verifies that no cache, counter or temporary state
// leaks into a subsequent success.
func TestArrayMatchRecoverableFailure(t *testing.T) {
	bad := []interface{}{
		"a",
		map[string]interface{}{"k": func() {}},
	}

	d := &Differ{}
	d.WithOpts(Factorize(), LCS())
	func() {
		defer func() {
			r := recover()
			if _, ok := r.(invalidJSONTypeError); !ok {
				t.Errorf("expected invalidJSONTypeError, got %T: %v", r, r)
			}
		}()
		d.Compare(bad, bad)
	}()

	// The comparison failed midway; Reset must wipe the
	// partial patch and every piece of derived state.
	d.Reset()
	if len(d.Patch()) != 0 {
		t.Errorf("partial patch left behind: %s", d.Patch())
	}
	if len(d.hashmap) != 0 {
		t.Errorf("factorization hashmap left behind: %d entries", len(d.hashmap))
	}
	if len(d.removals) != 0 || len(d.removalIdx) != 0 {
		t.Errorf("removal registry left behind: %d removals, %d buckets",
			len(d.removals), len(d.removalIdx))
	}
	if d.Stats() != (Stats{}) {
		t.Errorf("diagnostic counters left behind: %+v", d.Stats())
	}

	// The exact same valid input must produce the exact
	// same result as a brand-new Differ.
	tc := arrayMatchCase{
		src:  `{"a":[1,2],"b":[3,4]}`,
		tgt:  `{"a":[2,1],"b":[4,3]}`,
		opts: []Option{Factorize(), LCS()},
	}
	validSrc, validTgt := mustJSONValue(t, tc.src), mustJSONValue(t, tc.tgt)

	d.Compare(validSrc, validTgt)
	fresh := &Differ{}
	fresh.WithOpts(tc.opts...)
	fresh.Compare(validSrc, validTgt)

	if got, want := patchJSON(t, d.Patch()), patchJSON(t, fresh.Patch()); got != want {
		t.Errorf("recovered Differ differs from fresh Differ:\n got: %s\nwant: %s", got, want)
	}
	if d.Stats() != fresh.Stats() {
		t.Errorf("recovered Differ stats differ:\n got: %+v\nwant: %+v", d.Stats(), fresh.Stats())
	}

	// Retrying with the same input gives a stable result.
	d.Reset()
	d.Compare(validSrc, validTgt)
	if got := patchJSON(t, d.Patch()); got != patchJSON(t, fresh.Patch()) {
		t.Errorf("retry produced an unstable patch:\n got: %s\nwant: %s",
			got, patchJSON(t, fresh.Patch()))
	}

	// The public, recovering entry reports the existing
	// error layer, and does so repeatably.
	p1, err1 := CompareWithoutMarshal(bad, bad)
	p2, err2 := CompareWithoutMarshal(bad, bad)
	if p1 != nil || p2 != nil {
		t.Errorf("failed comparison must return a nil patch, got %v and %v", p1, p2)
	}
	if err1 == nil || err2 == nil || err1.Error() != err2.Error() {
		t.Errorf("expected identical non-nil errors, got %q and %q", err1, err2)
	}
	if want := "jsondiff: invalid json type: func()"; err1.Error() != want {
		t.Errorf("error layer mismatch:\n got: %q\nwant: %q", err1.Error(), want)
	}

	// A subsequent package-level comparison is unaffected.
	p3, err := Compare(validSrc, validTgt, tc.opts...)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := patchJSON(t, p3); got != patchJSON(t, fresh.Patch()) {
		t.Errorf("package-level comparison mismatch:\n got: %s\nwant: %s",
			got, patchJSON(t, fresh.Patch()))
	}
}

// TestArrayMatchConcurrentIsolation runs the fixed cases
// from many goroutines, released together by a barrier,
// and verifies that the results and diagnostic counters
// of every call equal the sequential baseline.
func TestArrayMatchConcurrentIsolation(t *testing.T) {
	const goroutines = 16

	type outcome struct {
		patch string
		stats Stats
		err   string
	}
	baseline := make([]outcome, len(arrayMatchCases))
	for i, tc := range arrayMatchCases {
		p, s := runArrayMatchCase(t, tc)
		baseline[i] = outcome{patch: patchJSON(t, p), stats: s}
	}

	results := make([][]outcome, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start

			local := make([]outcome, len(arrayMatchCases))
			for i, tc := range arrayMatchCases {
				src, tgt := mustJSONValue(nil, tc.src), mustJSONValue(nil, tc.tgt)
				p, err := Compare(src, tgt, tc.opts...)
				if err != nil {
					local[i].err = err.Error()
					continue
				}
				d := &Differ{}
				d.WithOpts(tc.opts...)
				d.Compare(src, tgt)
				local[i] = outcome{
					patch: patchJSON(nil, p),
					stats: d.Stats(),
				}
			}
			results[g] = local
		}(g)
	}
	close(start)
	wg.Wait()

	for g, local := range results {
		for i, got := range local {
			want := baseline[i]
			if got.err != "" {
				t.Errorf("goroutine %d case %q: unexpected error: %s",
					g, arrayMatchCases[i].name, got.err)
				continue
			}
			if got.patch != want.patch {
				t.Errorf("goroutine %d case %q: patch mismatch:\n got: %s\nwant: %s",
					g, arrayMatchCases[i].name, got.patch, want.patch)
			}
			if got.stats != want.stats {
				t.Errorf("goroutine %d case %q: stats leakage:\n got: %+v\nwant: %+v",
					g, arrayMatchCases[i].name, got.stats, want.stats)
			}
		}
	}
}

// TestArrayMatchStableOrder runs the same inputs while
// changing only result-independent conditions: fresh
// versus retained cache/registry state, iteration order
// and scheduling. The patch and its order must remain
// byte-for-byte stable.
func TestArrayMatchStableOrder(t *testing.T) {
	baseline := make([]string, len(arrayMatchCases))
	for i, tc := range arrayMatchCases {
		p, _ := runArrayMatchCase(t, tc)
		baseline[i] = patchJSON(t, p)
	}

	// Fresh Differ instances exercise randomized map
	// enumeration; reused Differ instances exercise the
	// retained cache/registry storage.
	reused := &Differ{}
	for round := 0; round < 25; round++ {
		for i, tc := range arrayMatchCases {
			var p Patch
			if round%2 == 0 {
				d := &Differ{}
				d.WithOpts(tc.opts...)
				d.Compare(mustJSONValue(t, tc.src), mustJSONValue(t, tc.tgt))
				p = d.Patch()
			} else {
				reused.Reset()
				reused.WithOpts(tc.opts...)
				reused.Compare(mustJSONValue(t, tc.src), mustJSONValue(t, tc.tgt))
				p = reused.Patch()
			}
			if got := patchJSON(t, p); got != baseline[i] {
				t.Errorf("round %d case %q: unstable order:\n got: %s\nwant: %s",
					round, tc.name, got, baseline[i])
			}
		}
	}

	// Changing only the dispatch order of the cases.
	order := rand.New(rand.NewSource(1)).Perm(len(arrayMatchCases))
	for _, i := range order {
		tc := arrayMatchCases[i]
		p, s := runArrayMatchCase(t, tc)
		if got := patchJSON(t, p); got != baseline[i] {
			t.Errorf("reordered case %q: unstable patch:\n got: %s\nwant: %s",
				tc.name, got, baseline[i])
		}
		if s != tc.wantStats {
			t.Errorf("reordered case %q: unstable stats: got %+v", tc.name, s)
		}
	}
}

// TestArrayMatchRandomizedFixedSeed feeds deterministic
// pseudo-random documents, biased toward duplicate and
// hash-colliding values, through the public entry. The
// patch must always apply back to the target, and the
// whole sequence (patches and counters) must be
// reproducible from the seed.
func TestArrayMatchRandomizedFixedSeed(t *testing.T) {
	const (
		seed       = 20260924
		iterations = 200
	)
	type record struct {
		patch string
		stats Stats
	}
	run := func() []record {
		rng := rand.New(rand.NewSource(seed))
		records := make([]record, 0, iterations)

		for i := 0; i < iterations; i++ {
			src := randomJSONValue(rng, 0)
			tgt := randomJSONValue(rng, 0)
			srcJSON := mustMarshal(t, src)
			tgtJSON := mustMarshal(t, tgt)

			patch, err := CompareJSON(srcJSON, tgtJSON, Factorize(), LCS())
			if err != nil {
				t.Fatalf("seed=%d iteration=%d: unexpected error: %v\nsrc=%s\ntgt=%s",
					seed, i, err, srcJSON, tgtJSON)
			}
			// The same input through the Differ entry must
			// agree with the package-level entry.
			d := &Differ{}
			d.WithOpts(Factorize(), LCS())
			d.Compare(mustJSONValue(t, string(srcJSON)), mustJSONValue(t, string(tgtJSON)))
			if got, want := patchJSON(t, d.Patch()), patchJSON(t, patch); got != want {
				t.Fatalf("seed=%d iteration=%d: entries disagree:\n got: %s\nwant: %s",
					seed, i, got, want)
			}
			records = append(records, record{patchJSON(t, patch), d.Stats()})

			out, err := patch.apply(srcJSON, true)
			if err != nil {
				t.Fatalf("seed=%d iteration=%d minimal repro:\nsrc=%s\ntgt=%s\npatch=%s\napply error: %v",
					seed, i, srcJSON, tgtJSON, patchJSON(t, patch), err)
			}
			if got, want := mustJSONValue(t, string(out)), mustJSONValue(t, string(tgtJSON)); !reflect.DeepEqual(got, want) {
				t.Fatalf("seed=%d iteration=%d minimal repro:\nsrc=%s\ntgt=%s\npatch=%s\ngot=%s",
					seed, i, srcJSON, tgtJSON, patchJSON(t, patch), out)
			}
		}
		return records
	}
	first := run()
	second := run()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("seed=%d: results are not reproducible across runs", seed)
	}
}

// randomJSONValue generates a deterministic pseudo-random
// JSON value. The leaf pool is intentionally small and
// contains values with colliding digests (null/false,
// ""/[]/{}) so that duplicates and collisions are
// exercised heavily.
func randomJSONValue(rng *rand.Rand, depth int) interface{} {
	leaves := []interface{}{
		nil, true, false,
		"", "x", "y",
		float64(0), float64(1), float64(2),
	}
	if depth >= 3 || rng.Intn(2) == 0 {
		return leaves[rng.Intn(len(leaves))]
	}
	if rng.Intn(2) == 0 {
		arr := make([]interface{}, rng.Intn(6))
		for i := range arr {
			arr[i] = randomJSONValue(rng, depth+1)
		}
		return arr
	}
	keys := []string{"a", "b", "c", "d"}
	obj := make(map[string]interface{}, rng.Intn(5))
	for i := 0; i < rng.Intn(5); i++ {
		obj[keys[rng.Intn(len(keys))]] = randomJSONValue(rng, depth+1)
	}
	return obj
}

func mustJSONValue(t *testing.T, s string) interface{} {
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		if t == nil {
			panic(err)
		}
		t.Fatalf("invalid JSON fixture: %v", err)
	}
	return v
}

func mustMarshal(t *testing.T, v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		if t == nil {
			panic(err)
		}
		t.Fatalf("failed to marshal value: %v", err)
	}
	return b
}

func patchJSON(t *testing.T, p Patch) string {
	return string(mustMarshal(t, p))
}

// assertPatchJSON compares the marshaled patch against
// the expected JSON and reports the first violating
// operation with its full context.
func assertPatchJSON(t *testing.T, got Patch, wantJSON string) {
	t.Helper()
	gotJSON := patchJSON(t, got)

	var g, w []interface{}
	if err := json.Unmarshal([]byte(gotJSON), &g); err != nil {
		t.Fatalf("failed to unmarshal got patch: %v", err)
	}
	if err := json.Unmarshal([]byte(wantJSON), &w); err != nil {
		t.Fatalf("failed to unmarshal want patch: %v", err)
	}
	for i := 0; i < len(g) || i < len(w); i++ {
		switch {
		case i >= len(g):
			t.Errorf("first violation at op %d: missing op\nwant: %v\nfull got:  %s\nfull want: %s",
				i, w[i], gotJSON, wantJSON)
			return
		case i >= len(w):
			t.Errorf("first violation at op %d: unexpected op %v\nfull got:  %s\nfull want: %s",
				i, g[i], gotJSON, wantJSON)
			return
		case !reflect.DeepEqual(g[i], w[i]):
			t.Errorf("first violation at op %d:\n got: %v\nwant: %v\nfull got:  %s\nfull want: %s",
				i, g[i], w[i], gotJSON, wantJSON)
			return
		}
	}
}

// assertAppliesTo applies the patch to the source
// document and checks that the result is the target.
func assertAppliesTo(t *testing.T, p Patch, src, tgt string) {
	t.Helper()
	out, err := p.apply([]byte(src), true)
	if err != nil {
		t.Fatalf("failed to apply patch: %v\npatch: %s", err, patchJSON(t, p))
	}
	if got, want := mustJSONValue(t, string(out)), mustJSONValue(t, tgt); !reflect.DeepEqual(got, want) {
		t.Errorf("patched document mismatch:\n got: %s\nwant: %s\npatch: %s",
			out, tgt, patchJSON(t, p))
	}
}

// assertMoveCopySources verifies that every move/copy
// operation references a source location whose value is
// deeply equal to the operation value, i.e. that no hash
// collision paired distinct values.
func assertMoveCopySources(t *testing.T, p Patch, src string) {
	t.Helper()
	srcBytes := []byte(src)

	for i, op := range p {
		if op.Type != OperationMove && op.Type != OperationCopy {
			continue
		}
		dp, err := toDotPath(op.From, srcBytes)
		if err != nil {
			t.Errorf("op %d (%s): invalid from path %q: %v", i, op.Type, op.From, err)
			continue
		}
		r := gjson.GetBytes(srcBytes, dp)
		if !r.Exists() {
			t.Errorf("op %d (%s): from path %q not found in source", i, op.Type, op.From)
			continue
		}
		if fromVal := mustJSONValue(t, r.Raw); !deepEqual(fromVal, op.Value) {
			t.Errorf("op %d (%s): mismatched source value:\nfrom %q = %s\nop value = %s",
				i, op.Type, op.From, r.Raw, mustMarshal(t, op.Value))
		}
	}
}
