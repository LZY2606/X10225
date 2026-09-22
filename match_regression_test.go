package jsondiff

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// This file contains the regression material dedicated to
// "jsondiff: unified array matching and move/copy candidate
// accounting". Every case enters through the official public
// entry points (Compare/CompareJSON/CompareWithoutMarshal);
// no internal result object is forged. Internal invariants are
// read through a small, package-private diagnostic seam that
// shares the production matching logic.

// ---- test infrastructure -------------------------------------------------

type jsonOp = map[string]interface{}

// arrayGuardCase is one fixed-input guard with the public
// patch the caller must observe and a quantified upper bound
// on the number of old/new semantic comparisons the unified
// matcher is allowed to perform.
type arrayGuardCase struct {
	name string
	src  interface{}
	tgt  interface{}
	opts []Option
	want []jsonOp
	// skipApply marks cases whose option set makes the
	// patch non-applicable to the literal target (e.g. the
	// Equivalent option treats array order as irrelevant).
	skipApply bool
	// maxComparisons is the baseline threshold for the number
	// of old/new semantic comparisons (LCS/equivalence probes
	// + move/copy candidate probes + copy source probes).
	// It is deliberately fixed rather than "any finite value"
	// so a regression that recomputes candidates is caught.
	maxComparisons int
}

// guardHooks is the test-only implementation of the internal
// matcherHooks seam. Both methods are optional; the production
// path never sets hooks, so this cannot change the algorithm.
type guardHooks struct {
	abort func(pointer, *arrayMatcher) bool
	dig   func(interface{}) (uint64, bool)
}

func (h *guardHooks) onMatcher(p pointer, m *arrayMatcher) {
	if h.abort != nil && h.abort(p, m) {
		panic(errCompareAborted)
	}
}

func (h *guardHooks) digest(v interface{}) (uint64, bool) {
	if h.dig != nil {
		return h.dig(v)
	}
	return 0, false
}

// withMatcherHooks is a package-private test option that
// installs the internal diagnostic seam. It is not exported
// and only attaches the same hook interface the production
// code checks, so comparisons still run the production path.
func withMatcherHooks(h matcherHooks) Option {
	return func(d *Differ) { d.hooks = h }
}

// runCompare enters through the public Differ.Compare entry
// point (the same path exercised by the package-level Compare
// and CompareJSON helpers) and returns the caller-visible
// patch together with the diagnostic counters.
func runCompare(t *testing.T, src, tgt interface{}, opts []Option, hooks *guardHooks) (Patch, matchStats) {
	t.Helper()
	d := &Differ{}
	d.WithOpts(opts...)
	if hooks != nil {
		d.hooks = hooks
	}
	d.targetBytes = mustMarshalGuard(t, tgt)
	d.Compare(src, tgt)
	return d.patch, d.stats
}

// runComparePublic enters exclusively through exported
// functions and also returns the per-invocation counters via
// a one-shot option-free Differ when needed; it exists to
// prove the acceptance story works with the real public API.
func runComparePublic(t *testing.T, src, tgt interface{}, opts []Option) Patch {
	t.Helper()
	patch, err := Compare(src, tgt, opts...)
	if err != nil {
		t.Fatalf("Compare returned error: %v", err)
	}
	return patch
}

func mustMarshalGuard(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// assertPatch reports the first violating position with
// enough context to reproduce it (op index, field, got/want).
func assertPatch(t *testing.T, got, want []jsonOp) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("first violation at op #%d: operation count got %d, want %d\n got: %s\nwant: %s",
			min(len(got), len(want)), len(got), len(want),
			marshalGuardOps(got), marshalGuardOps(want))
	}
	for i := range got {
		for _, field := range []string{"op", "path", "from", "value"} {
			gv, gok := got[i][field]
			wv, wok := want[i][field]
			if gok != wok || !reflect.DeepEqual(gv, wv) {
				t.Fatalf("first violation at op #%d field %q:\n got %#v\nwant %#v\nfull got: %s\nfull want: %s",
					i, field, gv, wv, marshalGuardOps(got), marshalGuardOps(want))
			}
		}
	}
}

func marshalGuardOps(ops []jsonOp) string {
	b, _ := json.Marshal(ops)
	return string(b)
}

// patchAsMaps serializes a Patch through its public JSON
// representation, i.e. exactly what callers consume.
func patchAsMaps(t *testing.T, p Patch) []jsonOp {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var ops []jsonOp
	if err := json.Unmarshal(b, &ops); err != nil {
		t.Fatal(err)
	}
	return ops
}

// applyGuardPatch verifies the patch round-trips src -> tgt.
func applyGuardPatch(t *testing.T, src interface{}, p Patch) []byte {
	t.Helper()
	b, err := p.apply(mustMarshalGuard(t, src), false)
	if err != nil {
		t.Fatalf("patch failed to apply: %v\npatch: %s", err, p)
	}
	return b
}

func normalizedGuard(t *testing.T, v interface{}) []byte {
	t.Helper()
	return mustMarshalGuard(t, v)
}

func guardArray(t *testing.T, s string) []interface{} {
	t.Helper()
	var v []interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// ---- fixed-input behavior guards -----------------------------------------

func TestArrayMatchGuards(t *testing.T) {
	cases := []arrayGuardCase{{
		name: "duplicated values are paired by identity not hash alone",
		src:  guardArray(t, `["a","b","a","y","c","c"]`),
		tgt:  guardArray(t, `["z","b","a","c","c","b"]`),
		opts: []Option{LCS(), Factorize()},
		want: []jsonOp{
			{"op": "replace", "path": "/0", "value": "z"},
			{"op": "remove", "path": "/3"},
			{"op": "copy", "from": "/1", "path": "/5"},
		},
		// 6x6 LCS table; the hash prefilter and the shared
		// memo keep semantic probes below the 72 that a
		// naive table+backtrack implementation requires.
		maxComparisons: 72,
	}, {
		name:           "deep objects are matched structurally under equivalence",
		src:            guardArray(t, `[{"k":{"a":[1,2]}},{"x":1},{"k":{"a":[2,1]}}]`),
		tgt:            guardArray(t, `[{"k":{"a":[2,1]}},{"k":{"a":[1,2]}},{"x":1}]`),
		opts:           []Option{LCS(), Factorize(), Equivalent()},
		want:           []jsonOp{},
		skipApply:      true,
		maxComparisons: 12,
	}, {
		name: "head insertion keeps stable indices",
		src:  guardArray(t, `["a","b","c"]`),
		tgt:  guardArray(t, `["x","a","b","c"]`),
		opts: []Option{LCS(), Factorize()},
		want: []jsonOp{
			{"op": "add", "path": "/0", "value": "x"},
		},
		maxComparisons: 20,
	}, {
		name: "tail insertion appends",
		src:  guardArray(t, `["a","b","c"]`),
		tgt:  guardArray(t, `["a","b","c","z"]`),
		opts: []Option{LCS(), Factorize()},
		want: []jsonOp{
			{"op": "add", "path": "/3", "value": "z"},
		},
		maxComparisons: 20,
	}, {
		name: "crossing move across the middle",
		src:  guardArray(t, `["a","b","c","d"]`),
		tgt:  guardArray(t, `["a","c","b","d"]`),
		opts: []Option{LCS(), Factorize()},
		want: []jsonOp{
			{"op": "move", "from": "/1", "path": "/2"},
		},
		maxComparisons: 24,
	}, {
		name: "bottom-up reorder emits ordered moves",
		src:  guardArray(t, `["a","b","c"]`),
		tgt:  guardArray(t, `["c","b","a"]`),
		opts: []Option{LCS(), Factorize()},
		want: []jsonOp{
			{"op": "move", "from": "/0", "path": "/2"},
			{"op": "move", "from": "/0", "path": "/1"},
		},
		maxComparisons: 24,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Drive through the public API as well, so the
			// guard cannot pass via forged internals.
			public := runComparePublic(t, tc.src, tc.tgt, tc.opts)

			patch, stats := runCompare(t, tc.src, tc.tgt, tc.opts, nil)

			// Both entry points must agree.
			if !reflect.DeepEqual(patchAsMaps(t, public), patchAsMaps(t, patch)) {
				t.Fatalf("public vs diagnostic entry disagree:\n%s\n%s", public, patch)
			}
			assertPatch(t, patchAsMaps(t, patch), tc.want)

			// Semantic guarantee: the patch rebuilds target,
			// unless the option set explicitly makes order
			// irrelevant (empty equivalence patch).
			if !tc.skipApply {
				patched := applyGuardPatch(t, tc.src, patch)
				if !bytes.Equal(patched, normalizedGuard(t, tc.tgt)) {
					t.Fatalf("patch does not rebuild target:\n got %s\nwant %s",
						patched, normalizedGuard(t, tc.tgt))
				}
			} else if len(patch) != 0 {
				t.Fatalf("expected empty equivalence patch, got %s", marshalGuardOps(patchAsMaps(t, patch)))
			}
			// Quantified comparison budget.
			total := stats.totalComparisons()
			if total > tc.maxComparisons {
				t.Errorf("comparison budget exceeded: %d > %d (probes=%d candidates=%d copy=%d filtered=%d collisions=%d memo=%d hashes=%d)",
					total, tc.maxComparisons,
					stats.eqProbes, stats.candidates, stats.copyProbes,
					stats.hashFiltered, stats.hashCollisions, stats.memoHits, stats.hashes)
			}
			t.Logf("stats: arrays=%d hashes=%d probes=%d filtered=%d candidates=%d copy=%d collisions=%d memoHits=%d total=%d",
				stats.arrays, stats.hashes, stats.eqProbes, stats.hashFiltered,
				stats.candidates, stats.copyProbes, stats.hashCollisions, stats.memoHits, total)
		})
	}
}

// TestArrayMatchGuard_DisabledOptimizations verifies that
// switching optimizations off falls back to the original
// semantics: positional ops without LCS, no move/copy without
// factorization.
func TestArrayMatchGuard_DisabledOptimizations(t *testing.T) {
	src := []interface{}{"a", "b", "c"}
	tgt := []interface{}{"c", "b", "a"}

	t.Run("plain positional semantics without LCS", func(t *testing.T) {
		patch := runComparePublic(t, src, tgt, nil)
		want := []jsonOp{
			{"op": "replace", "path": "/0", "value": "c"},
			{"op": "replace", "path": "/2", "value": "a"},
		}
		assertPatch(t, patchAsMaps(t, patch), want)
	})

	t.Run("LCS without factorization emits no move/copy", func(t *testing.T) {
		patch := runComparePublic(t, src, tgt, []Option{LCS()})
		for i, o := range patch {
			if o.Type == OperationMove || o.Type == OperationCopy {
				t.Fatalf("first violation at op #%d: optimization op %q leaked with Factorize off", i, o.Type)
			}
		}
		want := []jsonOp{
			{"op": "remove", "path": "/0"},
			{"op": "remove", "path": "/0"},
			{"op": "add", "path": "/1", "value": "b"},
			{"op": "add", "path": "/2", "value": "a"},
		}
		assertPatch(t, patchAsMaps(t, patch), want)
	})

	t.Run("factorization without LCS keeps positional move", func(t *testing.T) {
		s := []interface{}{"a", "b", "c"}
		g := []interface{}{"b", "c", "a"}
		patch := runComparePublic(t, s, g, []Option{Factorize()})
		ops := patchAsMaps(t, patch)
		// Positional mode pairs indexes; only tail-remove +
		// append factorizes. A pure reorder stays positional.
		want := []jsonOp{
			{"op": "replace", "path": "/0", "value": "b"},
			{"op": "replace", "path": "/1", "value": "c"},
			{"op": "replace", "path": "/2", "value": "a"},
		}
		assertPatch(t, ops, want)
		if patched := applyGuardPatch(t, s, patch); !bytes.Equal(patched, normalizedGuard(t, g)) {
			t.Fatalf("patch does not rebuild target: %s", patched)
		}
	})
}
